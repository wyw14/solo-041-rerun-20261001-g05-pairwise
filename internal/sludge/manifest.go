package sludge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/interlock"
	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/model"
)

type ManifestStatus string

const (
	ManifestOpen      ManifestStatus = "open"
	ManifestCompleted ManifestStatus = "completed"
	ManifestFailed    ManifestStatus = "failed"
)

// Receiver identifies the hauler taking delivery of the sludge.
type Receiver struct {
	Company string `json:"company"`
	Plate   string `json:"plate"`
	Driver  string `json:"driver"`
	Contact string `json:"contact,omitempty"`
}

// Manifest is the consignment note (外运联单) for one sludge export task.
type Manifest struct {
	ID                    uuid.UUID      `json:"id"`
	Number                uint64         `json:"number"`
	LineID                model.LineID   `json:"line_id"`
	BasinID               string         `json:"basin_id"`
	SequenceID            uuid.UUID      `json:"sequence_id"`
	SequenceNumber        uint64         `json:"sequence_number"`
	PumpID                string         `json:"pump_id"`
	Generation            uint64         `json:"generation"`
	EstimatedVolumeM3     float64        `json:"estimated_volume_m3"`
	Receiver              Receiver       `json:"receiver"`
	PlannedHandoverAt     time.Time      `json:"planned_handover_at"`
	ReservationID         string         `json:"reservation_id"`
	Status                ManifestStatus `json:"status"`
	ConfirmedFlowM3H      float64        `json:"confirmed_flow_m3_h,omitempty"`
	FailureReason         string         `json:"failure_reason,omitempty"`
	CompensationIDs       []uuid.UUID    `json:"compensation_ids,omitempty"`
	CompensationCompleted bool           `json:"compensation_completed"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	ClosedAt              *time.Time     `json:"closed_at,omitempty"`
}

type ManifestInput struct {
	LineID            model.LineID
	BasinID           string
	Sequence          Sequence
	EstimatedVolumeM3 float64
	Receiver          Receiver
	PlannedHandoverAt time.Time
	ReservationID     string
}

type ManifestFilter struct {
	LineID   model.LineID
	Status   ManifestStatus
	Receiver string
}

type ManifestBook struct {
	mu      sync.RWMutex
	next    uint64
	records map[uuid.UUID]Manifest
	pumps   *PumpController
	arbiter *interlock.Arbiter
	queue   *journal.CompensationQueue
}

func NewManifestBook(pumps *PumpController, arbiter *interlock.Arbiter, queue *journal.CompensationQueue) (*ManifestBook, error) {
	if pumps == nil || arbiter == nil || queue == nil {
		return nil, errors.New("manifest book dependencies are required")
	}
	return &ManifestBook{next: 1, records: make(map[uuid.UUID]Manifest), pumps: pumps, arbiter: arbiter, queue: queue}, nil
}

// Create opens a consignment note for a drain task whose valve route is
// already reserved and whose pump sequence owns the duty pump.
func (b *ManifestBook) Create(input ManifestInput, now time.Time) (Manifest, error) {
	if input.LineID == "" {
		return Manifest{}, errors.New("manifest process line is required")
	}
	basin := strings.TrimSpace(input.BasinID)
	if basin == "" {
		return Manifest{}, errors.New("manifest settling basin is required")
	}
	if input.Sequence.ID == uuid.Nil || input.Sequence.PumpID == "" || input.Sequence.Generation == 0 || input.Sequence.LineID != input.LineID {
		return Manifest{}, errors.New("manifest pump sequence is invalid")
	}
	if input.EstimatedVolumeM3 <= 0 {
		return Manifest{}, errors.New("manifest estimated volume must be positive")
	}
	receiver := Receiver{
		Company: strings.TrimSpace(input.Receiver.Company),
		Plate:   strings.TrimSpace(input.Receiver.Plate),
		Driver:  strings.TrimSpace(input.Receiver.Driver),
		Contact: strings.TrimSpace(input.Receiver.Contact),
	}
	if receiver.Company == "" || receiver.Plate == "" || receiver.Driver == "" {
		return Manifest{}, errors.New("manifest receiver identity is incomplete")
	}
	if input.PlannedHandoverAt.IsZero() {
		return Manifest{}, errors.New("manifest handover time is required")
	}
	if strings.TrimSpace(input.ReservationID) == "" {
		return Manifest{}, errors.New("manifest drain reservation is required")
	}
	if ownerSequence, _, assigned := b.pumps.Assignment(input.Sequence.PumpID); !assigned || ownerSequence != input.Sequence.ID {
		return Manifest{}, errors.New("manifest pump is not assigned to the sequence")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, existing := range b.records {
		if existing.ReservationID == input.ReservationID && existing.Status == ManifestOpen {
			return Manifest{}, errors.New("open manifest already exists for the drain task")
		}
	}
	manifest := Manifest{
		ID: uuid.New(), Number: b.next, LineID: input.LineID, BasinID: basin,
		SequenceID: input.Sequence.ID, SequenceNumber: input.Sequence.Number,
		PumpID: input.Sequence.PumpID, Generation: input.Sequence.Generation,
		EstimatedVolumeM3: input.EstimatedVolumeM3, Receiver: receiver,
		PlannedHandoverAt: input.PlannedHandoverAt.UTC(), ReservationID: input.ReservationID,
		Status: ManifestOpen, CompensationIDs: make([]uuid.UUID, 0, 1),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	b.next++
	b.records[manifest.ID] = manifest
	return manifest, nil
}

// Complete closes the note only after the drain valve reservation has been
// released and the duty pump flow is still confirmed for the sequence.
func (b *ManifestBook) Complete(id uuid.UUID, now time.Time) (Manifest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	manifest, ok := b.records[id]
	if !ok {
		return Manifest{}, errors.New("manifest is not found")
	}
	if manifest.Status != ManifestOpen {
		return Manifest{}, errors.New("manifest is not open")
	}
	var gates []error
	if b.arbiter.IsHeld(manifest.ReservationID) {
		gates = append(gates, errors.New("drain resources are not released"))
	}
	flow, confirmed := b.pumps.OwnedFlow(manifest.PumpID, manifest.SequenceID)
	if !confirmed {
		gates = append(gates, errors.New("duty pump flow is not confirmed"))
	}
	if len(gates) > 0 {
		return Manifest{}, errors.Join(gates...)
	}
	closed := now.UTC()
	manifest.Status = ManifestCompleted
	manifest.ConfirmedFlowM3H = flow
	manifest.ClosedAt = &closed
	manifest.UpdatedAt = closed
	b.records[id] = manifest
	return manifest, nil
}

// Fail keeps the failure reason on the note and links a stop-pump
// compensation so the failed export task is unwound.
func (b *ManifestBook) Fail(id uuid.UUID, reason string, now time.Time) (Manifest, journal.Compensation, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Manifest{}, journal.Compensation{}, errors.New("manifest failure reason is required")
	}
	if len(reason) > 500 {
		return Manifest{}, journal.Compensation{}, errors.New("manifest failure reason is too long")
	}
	b.mu.Lock()
	manifest, ok := b.records[id]
	if !ok {
		b.mu.Unlock()
		return Manifest{}, journal.Compensation{}, errors.New("manifest is not found")
	}
	if manifest.Status != ManifestOpen {
		b.mu.Unlock()
		return Manifest{}, journal.Compensation{}, errors.New("manifest is not open")
	}
	manifest.Status = ManifestFailed
	manifest.FailureReason = reason
	manifest.UpdatedAt = now.UTC()
	b.records[id] = manifest
	b.mu.Unlock()

	compensation, err := journal.NewCompensationRef(
		manifest.LineID, manifest.SequenceID, manifest.Generation, manifest.PumpID, "stop",
		now.Add(time.Second), journal.RefManifest, manifest.ID,
	)
	if err != nil {
		return Manifest{}, journal.Compensation{}, err
	}
	b.queue.Schedule(compensation)

	b.mu.Lock()
	defer b.mu.Unlock()
	manifest = b.records[id]
	manifest.CompensationIDs = append(manifest.CompensationIDs, compensation.ID)
	manifest.UpdatedAt = now.UTC()
	b.records[id] = manifest
	return manifest, compensation, nil
}

// ExecuteCompensation runs a manifest-linked compensation (stopping the duty
// pump) and records completion on the note.
func (b *ManifestBook) ExecuteCompensation(ctx context.Context, compensation journal.Compensation, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if compensation.RefType != journal.RefManifest {
		return errors.New("compensation is not linked to a manifest")
	}
	if compensation.Action != "stop" {
		return errors.New("unsupported manifest compensation action")
	}
	if _, err := b.pumps.StopOwned(compensation.DeviceID, compensation.SequenceID, compensation.Generation, now); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	manifest, ok := b.records[compensation.RefID]
	if !ok {
		return errors.New("compensation manifest is not found")
	}
	for _, pendingID := range manifest.CompensationIDs {
		if pendingID == compensation.ID {
			manifest.CompensationCompleted = true
			closed := now.UTC()
			manifest.ClosedAt = &closed
			manifest.UpdatedAt = closed
			b.records[manifest.ID] = manifest
			return nil
		}
	}
	return errors.New("compensation is not linked to the manifest record")
}

func (b *ManifestBook) Get(id uuid.UUID) (Manifest, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	manifest, ok := b.records[id]
	return manifest, ok
}

func (b *ManifestBook) List(filter ManifestFilter) []Manifest {
	b.mu.RLock()
	defer b.mu.RUnlock()
	receiver := strings.ToLower(strings.TrimSpace(filter.Receiver))
	result := make([]Manifest, 0, len(b.records))
	for _, manifest := range b.records {
		if filter.LineID != "" && manifest.LineID != filter.LineID {
			continue
		}
		if filter.Status != "" && manifest.Status != filter.Status {
			continue
		}
		if receiver != "" {
			haystack := strings.ToLower(manifest.Receiver.Company + " " + manifest.Receiver.Plate + " " + manifest.Receiver.Driver)
			if !strings.Contains(haystack, receiver) {
				continue
			}
		}
		result = append(result, manifest)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Number < result[j].Number
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}
