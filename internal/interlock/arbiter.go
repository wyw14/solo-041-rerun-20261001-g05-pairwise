package interlock

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Arbiter struct {
	mu        sync.Mutex
	resources map[string]Reservation
	requests  map[string]Reservation
}

func NewArbiter() *Arbiter {
	return &Arbiter{resources: make(map[string]Reservation), requests: make(map[string]Reservation)}
}

func (a *Arbiter) Reserve(request Request, now time.Time) (Reservation, error) {
	if request.ID.String() == "00000000-0000-0000-0000-000000000000" {
		return Reservation{}, errors.New("interlock request is not initialized")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if current, exists := a.requests[request.ID.String()]; exists {
		return current, nil
	}
	for _, resource := range request.Resources {
		if owner, occupied := a.resources[resource]; occupied {
			return Reservation{}, fmt.Errorf("interlock resource %s is held by %s", resource, owner.Request.RouteID)
		}
	}
	reservation := Reservation{Request: request, ReservedAt: now.UTC()}
	for _, resource := range request.Resources {
		a.resources[resource] = reservation
	}
	a.requests[request.ID.String()] = reservation
	return reservation, nil
}

func (a *Arbiter) Release(requestID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	reservation, exists := a.requests[requestID]
	if !exists {
		return errors.New("interlock reservation is not found")
	}
	for _, resource := range reservation.Request.Resources {
		if owner, ok := a.resources[resource]; ok && owner.Request.ID == reservation.Request.ID {
			delete(a.resources, resource)
		}
	}
	delete(a.requests, requestID)
	return nil
}

func (a *Arbiter) Active() []Reservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]Reservation, 0, len(a.requests))
	for _, reservation := range a.requests {
		result = append(result, reservation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ReservedAt.Before(result[j].ReservedAt) })
	return result
}

// Holds 判断某互锁预约（按请求 ID）是否仍被持有。
func (a *Arbiter) Holds(requestID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, exists := a.requests[requestID]
	return exists
}

func (a *Arbiter) IsAvailable(resources []string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, resource := range resources {
		if _, occupied := a.resources[resource]; occupied {
			return false
		}
	}
	return true
}
