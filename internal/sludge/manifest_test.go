package sludge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/interlock"
	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/model"
)

func newManifestFixture(t *testing.T) (*ManifestBook, *interlock.Arbiter, *journal.CompensationQueue, *PumpController, time.Time) {
	t.Helper()
	pumps, err := NewPumpController([]Pump{
		{ID: "pump-line-1-A", LineID: "line-1", Kind: model.DevicePump},
		{ID: "pump-line-1-B", LineID: "line-1", Kind: model.DevicePump},
	})
	if err != nil {
		t.Fatalf("pump controller: %v", err)
	}
	arbiter := interlock.NewArbiter()
	queue := journal.NewCompensationQueue()
	book, err := NewManifestBook(pumps, arbiter, queue)
	if err != nil {
		t.Fatalf("manifest book: %v", err)
	}
	return book, arbiter, queue, pumps, time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
}

func validInput(interlockID string) ManifestInput {
	return ManifestInput{
		LineID: "line-1", BasinID: "SD1", PumpIDs: []string{"pump-line-1-A", "pump-line-1-B"},
		EstimatedVolume: 12.5, Receiver: "城东污泥处置中心", Vehicle: "苏A·12345",
		InterlockID: interlockID, HandoverAt: time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC),
	}
}

func reserveDrainInterlock(t *testing.T, arbiter *interlock.Arbiter, now time.Time) string {
	t.Helper()
	request, err := interlock.NewRequest(
		"line-1", interlock.OperationDrain, "drain-SD1",
		[]string{"line:line-1", "valve:drain:SD1"}, now,
	)
	if err != nil {
		t.Fatalf("interlock request: %v", err)
	}
	if _, err := arbiter.Reserve(request, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return request.ID.String()
}

func TestCreateManifestValidatesInput(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	cases := map[string]func(ManifestInput) ManifestInput{
		"missing line":        func(in ManifestInput) ManifestInput { in.LineID = ""; return in },
		"missing basin":       func(in ManifestInput) ManifestInput { in.BasinID = "  "; return in },
		"no pumps":            func(in ManifestInput) ManifestInput { in.PumpIDs = nil; return in },
		"unknown pump":        func(in ManifestInput) ManifestInput { in.PumpIDs = []string{"pump-nope"}; return in },
		"non-positive volume": func(in ManifestInput) ManifestInput { in.EstimatedVolume = 0; return in },
		"missing receiver":    func(in ManifestInput) ManifestInput { in.Receiver = ""; return in },
		"missing handover":    func(in ManifestInput) ManifestInput { in.HandoverAt = time.Time{}; return in },
	}
	for name, mutate := range cases {
		if _, err := book.Create(mutate(validInput("")), now); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestManifestNumberByDay(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	first, err := book.Create(validInput(""), now)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := book.Create(validInput(""), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if first.Number != "WS-20260930-0001" || second.Number != "WS-20260930-0002" {
		t.Fatalf("unexpected numbers: %s, %s", first.Number, second.Number)
	}
}

func TestCompleteRequiresFlowConfirmation(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(""), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := book.Complete(manifest.ID, now); err == nil ||
		!strings.Contains(err.Error(), "pump flow") {
		t.Fatalf("expected flow confirmation error, got %v", err)
	}
	if _, err := book.ConfirmFlow(manifest.ID, "pump-line-1-A", 42, now); err != nil {
		t.Fatalf("confirm first flow: %v", err)
	}
	if _, err := book.Complete(manifest.ID, now); err == nil ||
		!strings.Contains(err.Error(), "pump flow") {
		t.Fatalf("expected remaining pump flow error, got %v", err)
	}
	if _, err := book.ConfirmFlow(manifest.ID, "pump-line-1-B", 40, now); err != nil {
		t.Fatalf("confirm second flow: %v", err)
	}
	completed, err := book.Complete(manifest.ID, now)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != ManifestCompleted || completed.ClosedAt == nil {
		t.Fatalf("manifest not completed: %+v", completed)
	}
	if _, err := book.Complete(manifest.ID, now); err == nil {
		t.Fatal("completed manifest must not close twice")
	}
}

func TestCompleteBlockedWhileInterlockHeld(t *testing.T) {
	book, arbiter, _, _, now := newManifestFixture(t)
	interlockID := reserveDrainInterlock(t, arbiter, now)
	manifest, err := book.Create(validInput(interlockID), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, pumpID := range manifest.PumpIDs {
		if _, err := book.ConfirmFlow(manifest.ID, pumpID, 30, now); err != nil {
			t.Fatalf("confirm flow: %v", err)
		}
	}
	if _, err := book.Complete(manifest.ID, now); err == nil ||
		!strings.Contains(err.Error(), "interlock") {
		t.Fatalf("expected interlock-held error, got %v", err)
	}
	if err := arbiter.Release(interlockID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := book.Complete(manifest.ID, now); err != nil {
		t.Fatalf("complete after release: %v", err)
	}
}

func TestFailRecordsReasonAndSchedulesCompensations(t *testing.T) {
	book, arbiter, queue, pumps, now := newManifestFixture(t)
	interlockID := reserveDrainInterlock(t, arbiter, now)
	manifest, err := book.Create(validInput(interlockID), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sequenceID := uuid.New()
	for _, pumpID := range manifest.PumpIDs {
		if _, err := pumps.Assign(pumpID, sequenceID, 1, now); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if _, err := pumps.Ready(pumpID, sequenceID, 1, 35, now); err != nil {
			t.Fatalf("ready: %v", err)
		}
	}
	if _, err := book.Fail(manifest.ID, "  接收方车辆未到，超时中止 ", now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	failed, ok := book.Get(manifest.ID)
	if !ok || failed.Status != ManifestFailed {
		t.Fatalf("manifest not failed: %+v", failed)
	}
	if failed.FailReason != "接收方车辆未到，超时中止" {
		t.Fatalf("reason not preserved: %q", failed.FailReason)
	}
	if len(failed.CompensationIDs) != len(manifest.PumpIDs)+1 {
		t.Fatalf("expected %d compensations, got %d", len(manifest.PumpIDs)+1, len(failed.CompensationIDs))
	}
	executed, err := queue.ExecuteDue(context.Background(), now, func(ctx context.Context, item journal.Compensation) error {
		return book.ExecuteCompensation(ctx, item, now)
	})
	if err != nil {
		t.Fatalf("execute compensations: %v", err)
	}
	if executed != len(manifest.PumpIDs)+1 {
		t.Fatalf("executed %d compensations", executed)
	}
	for _, pump := range pumps.Snapshot() {
		if pump.Running || pump.Flow != 0 {
			t.Fatalf("pump %s not released: %+v", pump.ID, pump)
		}
	}
	if arbiter.Holds(interlockID) {
		t.Fatal("drain interlock still held after compensation")
	}
}

func TestFailRequiresReason(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(""), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := book.Fail(manifest.ID, "   ", now); err == nil {
		t.Fatal("expected failure reason error")
	}
}

func TestListFiltersByLineAndStatus(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(""), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := book.Fail(manifest.ID, "test abort", now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	other := validInput("")
	other.LineID = "line-2"
	other.PumpIDs = []string{"pump-line-1-A"}
	if _, err := book.Create(other, now); err != nil {
		t.Fatalf("create other: %v", err)
	}
	if got := book.List(ManifestFilter{LineID: "line-1"}); len(got) != 1 {
		t.Fatalf("line filter returned %d manifests", len(got))
	}
	if got := book.List(ManifestFilter{Status: ManifestFailed}); len(got) != 1 || got[0].Status != ManifestFailed {
		t.Fatalf("status filter returned %+v", got)
	}
	if got := book.List(ManifestFilter{LineID: "line-1", Status: ManifestOpen}); len(got) != 0 {
		t.Fatalf("combined filter returned %d manifests", len(got))
	}
}

func TestRenderDocumentContainsKeyFields(t *testing.T) {
	book, _, _, _, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(""), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	document := manifest.RenderDocument()
	for _, want := range []string{manifest.Number, "line-1", "SD1", "城东污泥处置中心", "pump-line-1-A"} {
		if !strings.Contains(document, want) {
			t.Fatalf("document missing %q:\n%s", want, document)
		}
	}
}
