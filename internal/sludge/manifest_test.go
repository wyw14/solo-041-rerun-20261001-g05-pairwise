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

func newManifestFixture(t *testing.T) (*ManifestBook, *PumpController, *interlock.Arbiter, *journal.CompensationQueue, Sequence, string, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	lineID := model.LineID("line-1")
	pumps, err := NewPumpController([]Pump{
		{ID: "pump-line-1-A", LineID: lineID, Kind: model.DevicePump},
		{ID: "pump-line-1-B", LineID: lineID, Kind: model.DevicePump},
	})
	if err != nil {
		t.Fatalf("pump controller: %v", err)
	}
	queue := journal.NewCompensationQueue()
	if _, err := NewSequenceBook(pumps, queue); err != nil {
		t.Fatalf("sequence book: %v", err)
	}
	arbiter := interlock.NewArbiter()
	book, err := NewManifestBook(pumps, arbiter, queue)
	if err != nil {
		t.Fatalf("manifest book: %v", err)
	}
	sequence, err := bookFixtureSequence(pumps, lineID, now)
	if err != nil {
		t.Fatalf("sequence: %v", err)
	}
	request, err := interlock.NewRequest(lineID, interlock.OperationDrain, "drain-SD1",
		[]string{"line:line-1", "valve:drain:SD1"}, now)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	reservation, err := arbiter.Reserve(request, now)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return book, pumps, arbiter, queue, sequence, reservation.Request.ID.String(), now
}

func bookFixtureSequence(pumps *PumpController, lineID model.LineID, now time.Time) (Sequence, error) {
	sequence := Sequence{
		ID: uuid.New(), LineID: lineID, Number: 7, Generation: 1,
		PumpID: "pump-line-1-A", Status: SequenceSwitching, StartedAt: now, UpdatedAt: now,
	}
	if _, err := pumps.Assign(sequence.PumpID, sequence.ID, sequence.Generation, now); err != nil {
		return Sequence{}, err
	}
	return sequence, nil
}

func validInput(sequence Sequence, reservationID string, now time.Time) ManifestInput {
	return ManifestInput{
		LineID: "line-1", BasinID: "SD1", Sequence: sequence, EstimatedVolumeM3: 12.5,
		Receiver:          Receiver{Company: "环保运输公司", Plate: "沪D-12345", Driver: "张三"},
		PlannedHandoverAt: now.Add(2 * time.Hour), ReservationID: reservationID,
	}
}

func TestManifestCreateValidates(t *testing.T) {
	book, _, _, _, sequence, reservationID, now := newManifestFixture(t)

	cases := map[string]func(ManifestInput) ManifestInput{
		"missing basin":  func(in ManifestInput) ManifestInput { in.BasinID = "  "; return in },
		"bad volume":     func(in ManifestInput) ManifestInput { in.EstimatedVolumeM3 = 0; return in },
		"bad receiver":   func(in ManifestInput) ManifestInput { in.Receiver.Plate = ""; return in },
		"no handover":    func(in ManifestInput) ManifestInput { in.PlannedHandoverAt = time.Time{}; return in },
		"no reservation": func(in ManifestInput) ManifestInput { in.ReservationID = ""; return in },
		"foreign sequence": func(in ManifestInput) ManifestInput {
			in.Sequence.LineID = "line-2"
			return in
		},
	}
	for name, mutate := range cases {
		input := mutate(validInput(sequence, reservationID, now))
		if _, err := book.Create(input, now); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestManifestCompleteGate(t *testing.T) {
	book, pumps, arbiter, _, sequence, reservationID, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(sequence, reservationID, now), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Flow confirmed but drain resources are still held.
	if _, err := pumps.Ready(sequence.PumpID, sequence.ID, sequence.Generation, 42, now); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if _, err := book.Complete(manifest.ID, now); err == nil ||
		!strings.Contains(err.Error(), "drain resources are not released") {
		t.Fatalf("expected resource-release gate, got %v", err)
	}

	// Resources released but flow not confirmed (simulate stale ownership by resetting).
	if err := arbiter.Release(reservationID); err != nil {
		t.Fatalf("release: %v", err)
	}
	stopped, err := pumps.StopOwned(sequence.PumpID, sequence.ID, sequence.Generation, now)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Flow != 0 {
		t.Fatalf("pump should have no confirmed flow after stop")
	}
	if _, err := book.Complete(manifest.ID, now); err == nil ||
		!strings.Contains(err.Error(), "duty pump flow is not confirmed") {
		t.Fatalf("expected flow-confirmation gate, got %v", err)
	}

	// Both gates satisfied.
	if _, err := pumps.Ready(sequence.PumpID, sequence.ID, sequence.Generation, 42, now.Add(time.Minute)); err != nil {
		t.Fatalf("re-ready: %v", err)
	}
	closed, err := book.Complete(manifest.ID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if closed.Status != ManifestCompleted || closed.ConfirmedFlowM3H != 42 || closed.ClosedAt == nil {
		t.Fatalf("unexpected closed manifest: %+v", closed)
	}
	if _, err := book.Complete(manifest.ID, now); err == nil {
		t.Fatal("completed manifest must not close twice")
	}
}

func TestManifestCompleteAfterRelease(t *testing.T) {
	book, pumps, arbiter, _, sequence, reservationID, now := newManifestFixture(t)
	if err := arbiter.Release(reservationID); err != nil {
		t.Fatal(err)
	}
	manifest, err := book.Create(validInput(sequence, reservationID, now), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pumps.Ready(sequence.PumpID, sequence.ID, sequence.Generation, 30, now); err != nil {
		t.Fatalf("ready: %v", err)
	}
	closed, err := book.Complete(manifest.ID, now)
	if err != nil {
		t.Fatalf("complete with released reservation: %v", err)
	}
	if closed.Status != ManifestCompleted {
		t.Fatalf("expected completed, got %s", closed.Status)
	}
}

func TestManifestFailLinksCompensation(t *testing.T) {
	book, pumps, _, queue, sequence, reservationID, now := newManifestFixture(t)
	manifest, err := book.Create(validInput(sequence, reservationID, now), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pumps.Ready(sequence.PumpID, sequence.ID, sequence.Generation, 42, now); err != nil {
		t.Fatalf("ready: %v", err)
	}

	if _, _, err := book.Fail(manifest.ID, "  ", now); err == nil {
		t.Fatal("blank failure reason must be rejected")
	}
	failed, compensation, err := book.Fail(manifest.ID, "罐车未到场", now)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.Status != ManifestFailed || failed.FailureReason != "罐车未到场" {
		t.Fatalf("unexpected failed manifest: %+v", failed)
	}
	if compensation.RefType != journal.RefManifest || compensation.RefID != manifest.ID {
		t.Fatalf("compensation not linked to manifest: %+v", compensation)
	}
	if pending := queue.Pending(); len(pending) != 1 || pending[0].ID != compensation.ID {
		t.Fatalf("compensation not scheduled: %+v", pending)
	}

	// Due compensation stops the pump and is recorded on the manifest.
	executedAt := now.Add(time.Minute)
	executed, err := queue.ExecuteDue(context.Background(), executedAt, func(ctx context.Context, item journal.Compensation) error {
		return book.ExecuteCompensation(ctx, item, executedAt)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if executed != 1 {
		t.Fatalf("expected 1 executed compensation, got %d", executed)
	}
	stored, ok := book.Get(manifest.ID)
	if !ok {
		t.Fatal("manifest missing")
	}
	if !stored.CompensationCompleted || stored.ClosedAt == nil {
		t.Fatalf("completion not recorded: %+v", stored)
	}
	if flow, confirmed := pumps.OwnedFlow(sequence.PumpID, sequence.ID); confirmed || flow != 0 {
		t.Fatalf("pump should be stopped after compensation, flow=%v", flow)
	}
}

func TestManifestListFilter(t *testing.T) {
	book, _, arbiter, _, sequence, reservationID, now := newManifestFixture(t)
	first, err := book.Create(validInput(sequence, reservationID, now), now)
	if err != nil {
		t.Fatal(err)
	}
	// A second reservation for another create.
	request, err := interlock.NewRequest("line-1", interlock.OperationDrain, "drain-SD2",
		[]string{"valve:drain:SD2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	reservation2, err := arbiter.Reserve(request, now)
	if err != nil {
		t.Fatal(err)
	}
	input := validInput(sequence, reservation2.Request.ID.String(), now)
	input.BasinID = "SD2"
	input.Receiver.Company = "其他清运公司"
	if _, err := book.Create(input, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := book.List(ManifestFilter{}); len(got) != 2 || got[0].ID != first.ID {
		t.Fatalf("expected chronological list, got %+v", got)
	}
	if got := book.List(ManifestFilter{Receiver: "其他"}); len(got) != 1 {
		t.Fatalf("receiver filter returned %d", len(got))
	}
	if got := book.List(ManifestFilter{LineID: "line-2"}); len(got) != 0 {
		t.Fatalf("line filter returned %d", len(got))
	}
}
