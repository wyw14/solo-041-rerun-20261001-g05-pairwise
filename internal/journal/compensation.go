package journal

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

type CompensationRef string

const (
	RefSequence CompensationRef = "sludge_sequence"
	RefManifest CompensationRef = "sludge_manifest"
)

type Compensation struct {
	ID         uuid.UUID       `json:"id"`
	LineID     model.LineID    `json:"line_id"`
	SequenceID uuid.UUID       `json:"sequence_id"`
	Generation uint64          `json:"generation"`
	DeviceID   string          `json:"device_id"`
	Action     string          `json:"action"`
	DueAt      time.Time       `json:"due_at"`
	RefType    CompensationRef `json:"ref_type"`
	RefID      uuid.UUID       `json:"ref_id"`
}

// NewCompensation builds a compensation linked to a sludge pump sequence.
func NewCompensation(lineID model.LineID, sequenceID uuid.UUID, generation uint64, deviceID, action string, dueAt time.Time) (Compensation, error) {
	return NewCompensationRef(lineID, sequenceID, generation, deviceID, action, dueAt, RefSequence, sequenceID)
}

// NewCompensationRef builds a compensation linked back to the workflow object
// that requested it (a pump sequence or a sludge export manifest).
func NewCompensationRef(lineID model.LineID, sequenceID uuid.UUID, generation uint64, deviceID, action string, dueAt time.Time, refType CompensationRef, refID uuid.UUID) (Compensation, error) {
	if lineID == "" || sequenceID == uuid.Nil || generation == 0 || deviceID == "" || action == "" {
		return Compensation{}, errors.New("compensation identity is incomplete")
	}
	if refType != RefSequence && refType != RefManifest {
		return Compensation{}, errors.New("compensation reference type is unsupported")
	}
	if refID == uuid.Nil {
		return Compensation{}, errors.New("compensation reference is required")
	}
	return Compensation{
		ID: uuid.New(), LineID: lineID, SequenceID: sequenceID, Generation: generation,
		DeviceID: deviceID, Action: action, DueAt: dueAt.UTC(), RefType: refType, RefID: refID,
	}, nil
}

type CompensationExecutor func(context.Context, Compensation) error

type CompensationQueue struct {
	mu      sync.Mutex
	pending []Compensation
}

func NewCompensationQueue() *CompensationQueue {
	return &CompensationQueue{pending: make([]Compensation, 0, 8)}
}

func (q *CompensationQueue) Schedule(item Compensation) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, item)
	sort.SliceStable(q.pending, func(i, j int) bool { return q.pending[i].DueAt.Before(q.pending[j].DueAt) })
}

func (q *CompensationQueue) ExecuteDue(ctx context.Context, now time.Time, execute CompensationExecutor) (int, error) {
	if execute == nil {
		return 0, errors.New("compensation executor is required")
	}
	q.mu.Lock()
	due := make([]Compensation, 0, len(q.pending))
	remaining := q.pending[:0]
	for _, item := range q.pending {
		if !item.DueAt.After(now) {
			due = append(due, item)
		} else {
			remaining = append(remaining, item)
		}
	}
	q.pending = remaining
	q.mu.Unlock()
	for index, item := range due {
		if err := execute(ctx, item); err != nil {
			q.mu.Lock()
			q.pending = append(due[index:], q.pending...)
			q.mu.Unlock()
			return index, err
		}
	}
	return len(due), nil
}

func (q *CompensationQueue) Pending() []Compensation {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Compensation(nil), q.pending...)
}
