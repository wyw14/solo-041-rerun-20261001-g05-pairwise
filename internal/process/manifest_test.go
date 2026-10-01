package process

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

func TestManifestWorkflow(t *testing.T) {
	current := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return current }
	plant, err := NewPlant(t.TempDir(), []model.LineID{"line-1", "line-2"}, clock)
	if err != nil {
		t.Fatalf("plant: %v", err)
	}
	ctx := context.Background()
	const lineID = model.LineID("line-1")

	drain, err := plant.StartDrain(lineID, "SD1")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	sequence, err := plant.StartSludgeSequence(lineID, "pump-line-1-A", 1)
	if err != nil {
		t.Fatalf("sequence: %v", err)
	}

	receiver := sludge.Receiver{Company: "环保运输公司", Plate: "沪D-12345", Driver: "张三"}
	if _, err := plant.ConfirmSludgeFlow(lineID, sequence.ID, sequence.Generation, 42); err != nil {
		t.Fatalf("confirm flow: %v", err)
	}
	manifest, err := plant.OpenManifest(lineID, 8, receiver, current.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Gate: resources still held -> cannot close.
	if _, err := plant.CompleteManifest(manifest.ID); err == nil ||
		!strings.Contains(err.Error(), "drain resources are not released") {
		t.Fatalf("expected release gate, got %v", err)
	}
	if err := plant.ReleaseInterlock(drain.Reservation.Request.ID.String()); err != nil {
		t.Fatalf("release: %v", err)
	}
	closed, err := plant.CompleteManifest(manifest.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if closed.Status != sludge.ManifestCompleted || closed.ConfirmedFlowM3H != 42 {
		t.Fatalf("unexpected closed manifest: %+v", closed)
	}

	// A second note fails: reason retained and linked stop-pump compensation runs.
	drain2, err := plant.StartDrain(lineID, "SD2")
	if err != nil {
		t.Fatalf("drain2: %v", err)
	}
	sequence2, err := plant.StartSludgeSequence(lineID, "pump-line-1-B", 2)
	if err != nil {
		t.Fatalf("sequence2: %v", err)
	}
	if _, err := plant.ConfirmSludgeFlow(lineID, sequence2.ID, sequence2.Generation, 35); err != nil {
		t.Fatalf("confirm flow 2: %v", err)
	}
	manifest2, err := plant.OpenManifest(lineID, 6, receiver, current.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	failed, compensation, err := plant.FailManifest(manifest2.ID, "罐车未到场")
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.Status != sludge.ManifestFailed || compensation.RefType != journal.RefManifest {
		t.Fatalf("unexpected failure record: %+v", failed)
	}
	if err := plant.ReleaseInterlock(drain2.Reservation.Request.ID.String()); err != nil {
		t.Fatalf("release 2: %v", err)
	}
	current = current.Add(time.Minute)
	executed, err := plant.RunCompensations(ctx)
	if err != nil {
		t.Fatalf("compensations: %v", err)
	}
	if executed != 1 {
		t.Fatalf("expected 1 compensation executed, got %d", executed)
	}
	stored, ok := plant.Manifest(manifest2.ID)
	if !ok || !stored.CompensationCompleted {
		t.Fatalf("completion not recorded: %+v", stored)
	}

	// Query filters.
	if got := plant.Manifests(sludge.ManifestFilter{Status: sludge.ManifestCompleted}); len(got) != 1 {
		t.Fatalf("completed filter got %d", len(got))
	}
	if got := plant.Manifests(sludge.ManifestFilter{Receiver: "沪D"}); len(got) != 2 {
		t.Fatalf("receiver filter got %d", len(got))
	}
	if got := plant.Manifests(sludge.ManifestFilter{Status: sludge.ManifestFailed}); len(got) != 1 {
		t.Fatalf("failed filter got %d", len(got))
	}
}
