package sludge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/interlock"
	"github.com/wyw14/cry-97/internal/journal"
	"github.com/wyw14/cry-97/internal/model"
)

// ManifestStatus 外运联单的生命周期状态。
type ManifestStatus string

const (
	ManifestOpen      ManifestStatus = "open"
	ManifestCompleted ManifestStatus = "completed"
	ManifestFailed    ManifestStatus = "failed"
)

// 失败联单补偿动作：释放停运排泥泵、释放排泥阀路互锁。
const (
	CompensationReleasePump      = "release-pump"
	CompensationReleaseInterlock = "release-interlock"
)

// Manifest 记录一次污泥外运排泥任务。
type Manifest struct {
	ID              uuid.UUID          `json:"id"`
	Number          string             `json:"number"`
	LineID          model.LineID       `json:"line_id"`
	BasinID         string             `json:"basin_id"`
	PumpIDs         []string           `json:"pump_ids"`
	PumpFlows       map[string]float64 `json:"pump_flows,omitempty"`
	EstimatedVolume float64            `json:"estimated_volume"`
	Receiver        string             `json:"receiver"`
	Vehicle         string             `json:"vehicle,omitempty"`
	InterlockID     string             `json:"interlock_id,omitempty"`
	HandoverAt      time.Time          `json:"handover_at"`
	Status          ManifestStatus     `json:"status"`
	FailReason      string             `json:"fail_reason,omitempty"`
	CompensationIDs []uuid.UUID        `json:"compensation_ids,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	ClosedAt        *time.Time         `json:"closed_at,omitempty"`
}

// ManifestInput 为排泥任务建单参数。
type ManifestInput struct {
	LineID          model.LineID
	BasinID         string
	PumpIDs         []string
	EstimatedVolume float64
	Receiver        string
	Vehicle         string
	InterlockID     string
	HandoverAt      time.Time
}

// ManifestFilter 约束联单查询范围。
type ManifestFilter struct {
	LineID model.LineID
	Status ManifestStatus
}

// ManifestBook 管理联单的创建、查询、结单与失败补偿。
type ManifestBook struct {
	mu       sync.RWMutex
	pumps    *PumpController
	arbiter  *interlock.Arbiter
	queue    *journal.CompensationQueue
	byID     map[uuid.UUID]Manifest
	seqByDay map[string]uint64
}

func NewManifestBook(pumps *PumpController, arbiter *interlock.Arbiter, queue *journal.CompensationQueue) (*ManifestBook, error) {
	if pumps == nil || arbiter == nil || queue == nil {
		return nil, errors.New("manifest book requires pump controller, interlock arbiter and compensation queue")
	}
	return &ManifestBook{
		pumps: pumps, arbiter: arbiter, queue: queue,
		byID: make(map[uuid.UUID]Manifest), seqByDay: make(map[string]uint64),
	}, nil
}

func normalizePumps(pumpIDs []string) ([]string, error) {
	if len(pumpIDs) == 0 {
		return nil, errors.New("at least one sludge pump is required")
	}
	out := make([]string, 0, len(pumpIDs))
	seen := make(map[string]struct{})
	for _, raw := range pumpIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, errors.New("sludge pump identity is empty")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// Create 建立一张外运联单。
func (b *ManifestBook) Create(input ManifestInput, now time.Time) (Manifest, error) {
	if input.LineID == "" {
		return Manifest{}, errors.New("process line is required")
	}
	if strings.TrimSpace(input.BasinID) == "" {
		return Manifest{}, errors.New("settling basin is required")
	}
	pumpIDs, err := normalizePumps(input.PumpIDs)
	if err != nil {
		return Manifest{}, err
	}
	for _, pumpID := range pumpIDs {
		if !b.pumps.Exists(pumpID) {
			return Manifest{}, fmt.Errorf("sludge pump %s is not found", pumpID)
		}
	}
	if input.EstimatedVolume <= 0 {
		return Manifest{}, errors.New("estimated sludge volume must be positive")
	}
	if strings.TrimSpace(input.Receiver) == "" {
		return Manifest{}, errors.New("sludge receiver is required")
	}
	if input.HandoverAt.IsZero() {
		return Manifest{}, errors.New("handover time is required")
	}
	now = now.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	day := now.Format("20060102")
	b.seqByDay[day]++
	flows := make(map[string]float64, len(pumpIDs))
	for _, pumpID := range pumpIDs {
		flows[pumpID] = 0
	}
	manifest := Manifest{
		ID:              uuid.New(),
		Number:          fmt.Sprintf("WS-%s-%04d", day, b.seqByDay[day]),
		LineID:          input.LineID,
		BasinID:         strings.TrimSpace(input.BasinID),
		PumpIDs:         pumpIDs,
		PumpFlows:       flows,
		EstimatedVolume: input.EstimatedVolume,
		Receiver:        strings.TrimSpace(input.Receiver),
		Vehicle:         strings.TrimSpace(input.Vehicle),
		InterlockID:     strings.TrimSpace(input.InterlockID),
		HandoverAt:      input.HandoverAt.UTC(),
		Status:          ManifestOpen,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	b.byID[manifest.ID] = manifest
	return manifest, nil
}

// Get 按 ID 查询联单。
func (b *ManifestBook) Get(id uuid.UUID) (Manifest, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	manifest, ok := b.byID[id]
	return manifest, ok
}

// List 按过滤条件返回联单，最早建单的在前。
func (b *ManifestBook) List(filter ManifestFilter) []Manifest {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Manifest, 0, len(b.byID))
	for _, manifest := range b.byID {
		if filter.LineID != "" && manifest.LineID != filter.LineID {
			continue
		}
		if filter.Status != "" && manifest.Status != filter.Status {
			continue
		}
		result = append(result, manifest)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].Number < result[j].Number
	})
	return result
}

// ConfirmFlow 记录任务中某台泵已确认的瞬时流量。
func (b *ManifestBook) ConfirmFlow(id uuid.UUID, pumpID string, flow float64, now time.Time) (Manifest, error) {
	if id == uuid.Nil {
		return Manifest{}, errors.New("manifest id is required")
	}
	pumpID = strings.TrimSpace(pumpID)
	if pumpID == "" {
		return Manifest{}, errors.New("pump identity is required")
	}
	if flow <= 0 {
		return Manifest{}, errors.New("confirmed pump flow must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	manifest, ok := b.byID[id]
	if !ok {
		return Manifest{}, errors.New("sludge manifest is not found")
	}
	if manifest.Status != ManifestOpen {
		return Manifest{}, fmt.Errorf("manifest %s is %s", manifest.Number, manifest.Status)
	}
	if _, ok := manifest.PumpFlows[pumpID]; !ok {
		return Manifest{}, fmt.Errorf("pump %s is not part of manifest %s", pumpID, manifest.Number)
	}
	manifest.PumpFlows[pumpID] = flow
	manifest.UpdatedAt = now.UTC()
	b.byID[id] = manifest
	return manifest, nil
}

func (m Manifest) allFlowsConfirmed() bool {
	for _, pumpID := range m.PumpIDs {
		if m.PumpFlows[pumpID] <= 0 {
			return false
		}
	}
	return true
}

func (m Manifest) drainResources() []string {
	return []string{"line:" + string(m.LineID), "valve:drain:" + m.BasinID}
}

// Complete 在排泥资源释放且全部泵流量确认后结单。
func (b *ManifestBook) Complete(id uuid.UUID, now time.Time) (Manifest, error) {
	if id == uuid.Nil {
		return Manifest{}, errors.New("manifest id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	manifest, ok := b.byID[id]
	if !ok {
		return Manifest{}, errors.New("sludge manifest is not found")
	}
	if manifest.Status != ManifestOpen {
		return Manifest{}, fmt.Errorf("manifest %s is %s", manifest.Number, manifest.Status)
	}
	if manifest.InterlockID != "" && b.arbiter.Holds(manifest.InterlockID) {
		return Manifest{}, errors.New("cannot close manifest while drain interlock is held")
	}
	if !b.arbiter.IsAvailable(manifest.drainResources()) {
		return Manifest{}, errors.New("cannot close manifest while drain route resources are occupied")
	}
	if !manifest.allFlowsConfirmed() {
		return Manifest{}, errors.New("cannot close manifest before every listed pump flow is confirmed")
	}
	closed := now.UTC()
	manifest.Status = ManifestCompleted
	manifest.ClosedAt = &closed
	manifest.UpdatedAt = closed
	b.byID[id] = manifest
	return manifest, nil
}

// Fail 保留失败原因，并为该任务调度泵与阀路释放的补偿动作。
func (b *ManifestBook) Fail(id uuid.UUID, reason string, now time.Time) (Manifest, error) {
	if id == uuid.Nil {
		return Manifest{}, errors.New("manifest id is required")
	}
	if strings.TrimSpace(reason) == "" {
		return Manifest{}, errors.New("failure reason is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	manifest, ok := b.byID[id]
	if !ok {
		return Manifest{}, errors.New("sludge manifest is not found")
	}
	if manifest.Status != ManifestOpen {
		return Manifest{}, fmt.Errorf("manifest %s is %s", manifest.Number, manifest.Status)
	}
	compensations := make([]journal.Compensation, 0, len(manifest.PumpIDs)+1)
	dueAt := now.UTC()
	for _, pumpID := range manifest.PumpIDs {
		compensation, err := journal.NewTaskCompensation(manifest.LineID, manifest.ID, pumpID, CompensationReleasePump, dueAt)
		if err != nil {
			return Manifest{}, err
		}
		b.queue.Schedule(compensation)
		compensations = append(compensations, compensation)
	}
	if manifest.InterlockID != "" && b.arbiter.Holds(manifest.InterlockID) {
		compensation, err := journal.NewTaskCompensation(manifest.LineID, manifest.ID, manifest.InterlockID, CompensationReleaseInterlock, dueAt)
		if err != nil {
			return Manifest{}, err
		}
		b.queue.Schedule(compensation)
		compensations = append(compensations, compensation)
	}
	manifest.Status = ManifestFailed
	manifest.FailReason = strings.TrimSpace(reason)
	manifest.CompensationIDs = make([]uuid.UUID, 0, len(compensations))
	for _, compensation := range compensations {
		manifest.CompensationIDs = append(manifest.CompensationIDs, compensation.ID)
	}
	manifest.UpdatedAt = dueAt
	b.byID[id] = manifest
	return manifest, nil
}

// ExecuteCompensation 执行失败联单关联的补偿动作。
func (b *ManifestBook) ExecuteCompensation(ctx context.Context, compensation journal.Compensation, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch compensation.Action {
	case CompensationReleasePump:
		_, err := b.pumps.Release(compensation.DeviceID, now)
		return err
	case CompensationReleaseInterlock:
		if !b.arbiter.Holds(compensation.DeviceID) {
			return nil
		}
		return b.arbiter.Release(compensation.DeviceID)
	default:
		return fmt.Errorf("unsupported manifest compensation action %q", compensation.Action)
	}
}
