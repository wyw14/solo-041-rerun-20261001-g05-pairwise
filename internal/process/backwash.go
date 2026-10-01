package process

import (
	"errors"
	"time"

	"github.com/wyw14/cry-97/internal/interlock"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

type BackwashOperation struct {
	LineID      model.LineID          `json:"line_id"`
	FilterID    string                `json:"filter_id"`
	Reservation interlock.Reservation `json:"reservation"`
	StartedAt   time.Time             `json:"started_at"`
}

func (p *Plant) StartBackwash(lineID model.LineID, filterID string) (BackwashOperation, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency || filterID == "" {
		return BackwashOperation{}, errors.New("process line cannot start backwash")
	}
	now := p.now()
	request, err := interlock.NewRequest(
		lineID, interlock.OperationBackwash, "backwash-"+filterID,
		[]string{"line:" + string(lineID), "valve:backwash:" + filterID}, now,
	)
	if err != nil {
		return BackwashOperation{}, err
	}
	if !p.interlocks.IsAvailable(request.Resources) {
		return BackwashOperation{}, errors.New("backwash route is occupied")
	}
	reservation, err := p.interlocks.Reserve(request, now)
	if err != nil {
		return BackwashOperation{}, err
	}
	return BackwashOperation{LineID: lineID, FilterID: filterID, Reservation: reservation, StartedAt: now.UTC()}, nil
}

func (p *Plant) StartDrain(lineID model.LineID, basinID string) (sludge.DrainOperation, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return sludge.DrainOperation{}, errors.New("process line cannot start drain")
	}
	return p.drains.Start(lineID, basinID, p.now())
}

func (p *Plant) ReleaseInterlock(requestID string) error {
	if err := p.drains.Release(requestID, p.now()); err == nil {
		return nil
	}
	return p.interlocks.Release(requestID)
}

func (p *Plant) Interlocks() []interlock.Reservation {
	return p.interlocks.Active()
}
