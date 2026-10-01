package process

import (
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

func (p *Plant) CreateSludgeManifest(input sludge.ManifestInput) (sludge.Manifest, error) {
	p.mu.RLock()
	state, ok := p.lines[input.LineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return sludge.Manifest{}, errors.New("process line cannot create sludge manifest")
	}
	return p.manifests.Create(input, p.now())
}

func (p *Plant) ListSludgeManifests(lineID model.LineID, status sludge.ManifestStatus) []sludge.Manifest {
	return p.manifests.List(sludge.ManifestFilter{LineID: lineID, Status: status})
}

func (p *Plant) GetSludgeManifest(id uuid.UUID) (sludge.Manifest, bool) {
	return p.manifests.Get(id)
}

func (p *Plant) ConfirmManifestFlow(id uuid.UUID, pumpID string, flow float64) (sludge.Manifest, error) {
	return p.manifests.ConfirmFlow(id, pumpID, flow, p.now())
}

func (p *Plant) CompleteSludgeManifest(id uuid.UUID) (sludge.Manifest, error) {
	return p.manifests.Complete(id, p.now())
}

func (p *Plant) FailSludgeManifest(id uuid.UUID, reason string) (sludge.Manifest, error) {
	return p.manifests.Fail(id, reason, p.now())
}
