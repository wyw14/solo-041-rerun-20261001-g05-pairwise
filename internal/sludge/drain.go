package sludge

import (
	"errors"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/interlock"
	"github.com/wyw14/cry-97/internal/model"
)

type DrainOperation struct {
	LineID      model.LineID          `json:"line_id"`
	BasinID     string                `json:"basin_id"`
	Reservation interlock.Reservation `json:"reservation"`
	StartedAt   time.Time             `json:"started_at"`
	ReleasedAt  *time.Time            `json:"released_at,omitempty"`
}

// Released reports whether the drain valve route has been handed back.
func (o DrainOperation) Released() bool {
	return o.ReleasedAt != nil
}

type DrainService struct {
	mu       sync.RWMutex
	arbiter  *interlock.Arbiter
	byLine   map[model.LineID]DrainOperation
	byRequID map[string]model.LineID
}

func NewDrainService(arbiter *interlock.Arbiter) (*DrainService, error) {
	if arbiter == nil {
		return nil, errors.New("drain service requires interlock arbiter")
	}
	return &DrainService{
		arbiter:  arbiter,
		byLine:   make(map[model.LineID]DrainOperation),
		byRequID: make(map[string]model.LineID),
	}, nil
}

func (s *DrainService) Start(lineID model.LineID, basinID string, now time.Time) (DrainOperation, error) {
	request, err := interlock.NewRequest(
		lineID, interlock.OperationDrain, "drain-"+basinID,
		[]string{"line:" + string(lineID), "valve:drain:" + basinID}, now,
	)
	if err != nil {
		return DrainOperation{}, err
	}
	if !s.arbiter.IsAvailable(request.Resources) {
		return DrainOperation{}, errors.New("drain route is occupied")
	}
	reservation, err := s.arbiter.Reserve(request, now)
	if err != nil {
		return DrainOperation{}, err
	}
	operation := DrainOperation{LineID: lineID, BasinID: basinID, Reservation: reservation, StartedAt: now.UTC()}
	s.mu.Lock()
	if previous, exists := s.byLine[lineID]; exists && previous.ReleasedAt == nil {
		s.mu.Unlock()
		_ = s.arbiter.Release(reservation.Request.ID.String())
		return DrainOperation{}, errors.New("drain operation for the line is already open")
	}
	s.byLine[lineID] = operation
	s.byRequID[reservation.Request.ID.String()] = lineID
	s.mu.Unlock()
	return operation, nil
}

// Release hands back the valve route reservation and records the release time.
func (s *DrainService) Release(requestID string, now time.Time) error {
	s.mu.Lock()
	lineID, tracked := s.byRequID[requestID]
	s.mu.Unlock()
	if !tracked {
		return errors.New("drain reservation is not found")
	}
	if err := s.arbiter.Release(requestID); err != nil {
		return err
	}
	released := now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if operation, ok := s.byLine[lineID]; ok && operation.Reservation.Request.ID.String() == requestID {
		operation.ReleasedAt = &released
		s.byLine[lineID] = operation
	}
	delete(s.byRequID, requestID)
	return nil
}

// Current returns the latest drain operation for a line, if any.
func (s *DrainService) Current(lineID model.LineID) (DrainOperation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	operation, ok := s.byLine[lineID]
	return operation, ok
}
