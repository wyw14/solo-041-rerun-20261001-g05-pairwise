package process

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

func (p *Plant) StartHandover(lineID model.LineID, fromPump, toPump string) (sludge.Handover, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return sludge.Handover{}, errors.New("process line cannot start pump handover")
	}
	return p.handovers.Start(lineID, fromPump, toPump, state.Generation, p.now())
}

func (p *Plant) ConfirmHandover(lineID model.LineID, pumpID string, generation uint64, flow float64) (sludge.Handover, error) {
	handover, err := p.handovers.ConfirmReady(lineID, pumpID, generation, flow, p.now())
	if err != nil {
		return sludge.Handover{}, err
	}
	p.mu.Lock()
	state := p.lines[lineID]
	state.UpdatedAt = p.now().UTC()
	p.lines[lineID] = state
	p.mu.Unlock()
	return handover, nil
}

func (p *Plant) StartSludgeSequence(lineID model.LineID, pumpID string, number uint64) (sludge.Sequence, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return sludge.Sequence{}, errors.New("process line cannot start sludge sequence")
	}
	return p.sequences.Start(lineID, pumpID, number, state.Generation, p.now())
}

func (p *Plant) ConfirmSludgeFlow(lineID model.LineID, sequenceID uuid.UUID, generation uint64, flow float64) (sludge.Sequence, error) {
	return p.sequences.ConfirmFlow(lineID, sequenceID, generation, flow, p.now())
}

func (p *Plant) FailSludgeSequence(lineID model.LineID) (sludge.Sequence, error) {
	return p.sequences.Fail(lineID, p.now())
}

// OpenManifest creates a sludge export consignment note for the active drain
// operation and pump sequence of a line.
func (p *Plant) OpenManifest(lineID model.LineID, volume float64, receiver sludge.Receiver, handoverAt time.Time) (sludge.Manifest, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return sludge.Manifest{}, errors.New("process line cannot open sludge manifest")
	}
	drain, ok := p.drains.Current(lineID)
	if !ok {
		return sludge.Manifest{}, errors.New("drain operation is not started")
	}
	if drain.Released() {
		return sludge.Manifest{}, errors.New("drain operation is already released")
	}
	sequence, ok := p.sequences.Current(lineID)
	if !ok {
		return sludge.Manifest{}, errors.New("sludge pump sequence is not active")
	}
	now := p.now()
	if handoverAt.IsZero() {
		handoverAt = now
	}
	return p.manifests.Create(sludge.ManifestInput{
		LineID: lineID, BasinID: drain.BasinID, Sequence: sequence,
		EstimatedVolumeM3: volume, Receiver: receiver, PlannedHandoverAt: handoverAt,
		ReservationID: drain.Reservation.Request.ID.String(),
	}, now)
}

func (p *Plant) CompleteManifest(id uuid.UUID) (sludge.Manifest, error) {
	return p.manifests.Complete(id, p.now())
}

func (p *Plant) FailManifest(id uuid.UUID, reason string) (sludge.Manifest, journal.Compensation, error) {
	return p.manifests.Fail(id, reason, p.now())
}

func (p *Plant) Manifests(filter sludge.ManifestFilter) []sludge.Manifest {
	return p.manifests.List(filter)
}

func (p *Plant) Manifest(id uuid.UUID) (sludge.Manifest, bool) {
	return p.manifests.Get(id)
}

func (p *Plant) RunCompensations(ctx context.Context) (int, error) {
	now := p.now()
	return p.compensations.ExecuteDue(ctx, now, func(callCtx context.Context, item journal.Compensation) error {
		switch item.RefType {
		case journal.RefManifest:
			return p.manifests.ExecuteCompensation(callCtx, item, now)
		default:
			return p.sequences.ExecuteCompensation(callCtx, item, now)
		}
	})
}

func (p *Plant) Pumps() []sludge.Pump {
	return p.pumps.Snapshot()
}
