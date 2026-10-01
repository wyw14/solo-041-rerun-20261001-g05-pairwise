package api

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

var (
	errInvalidManifestStatus = errors.New("manifest status filter is invalid")
	errManifestNotFound      = errors.New("sludge manifest is not found")
)

type manifestRequest struct {
	LineID          model.LineID `json:"line_id"`
	BasinID         string       `json:"basin_id"`
	PumpIDs         []string     `json:"pump_ids"`
	EstimatedVolume float64      `json:"estimated_volume"`
	Receiver        string       `json:"receiver"`
	Vehicle         string       `json:"vehicle"`
	InterlockID     string       `json:"interlock_id"`
	HandoverAt      string       `json:"handover_at"`
}

type manifestFlowRequest struct {
	PumpID string  `json:"pump_id"`
	Flow   float64 `json:"flow"`
}

type manifestFailRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) createManifest(w http.ResponseWriter, r *http.Request) {
	var input manifestRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	handoverAt, err := time.Parse(time.RFC3339, strings.TrimSpace(input.HandoverAt))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manifest, err := s.plant.CreateSludgeManifest(sludge.ManifestInput{
		LineID:          input.LineID,
		BasinID:         input.BasinID,
		PumpIDs:         input.PumpIDs,
		EstimatedVolume: input.EstimatedVolume,
		Receiver:        input.Receiver,
		Vehicle:         input.Vehicle,
		InterlockID:     input.InterlockID,
		HandoverAt:      handoverAt,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, manifest)
}

func (s *Server) listManifests(w http.ResponseWriter, r *http.Request) {
	var lineID model.LineID
	if raw := strings.TrimSpace(r.URL.Query().Get("line_id")); raw != "" {
		parsed, err := model.ParseLineID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		lineID = parsed
	}
	status := sludge.ManifestStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != sludge.ManifestOpen && status != sludge.ManifestCompleted && status != sludge.ManifestFailed {
		writeError(w, http.StatusBadRequest, errInvalidManifestStatus)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": s.plant.ListSludgeManifests(lineID, status)})
}

func (s *Server) loadManifest(w http.ResponseWriter, r *http.Request) (sludge.Manifest, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "manifestID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return sludge.Manifest{}, false
	}
	manifest, ok := s.plant.GetSludgeManifest(id)
	if !ok {
		writeError(w, http.StatusNotFound, errManifestNotFound)
		return sludge.Manifest{}, false
	}
	return manifest, true
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request) {
	if manifest, ok := s.loadManifest(w, r); ok {
		writeJSON(w, http.StatusOK, manifest)
	}
}

func (s *Server) printManifest(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.loadManifest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>%s</title>
<style>body{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:14px;line-height:1.8;padding:32px;color:#111;}
pre{white-space:pre-wrap;margin:0 0 24px;}
@media print{.no-print{display:none}}
</style></head><body><div class="no-print"><button onclick="window.print()" style="padding:8px 16px;margin-bottom:16px;">打印</button></div>
<pre>%s</pre><script>window.addEventListener('load',function(){window.print();});</script></body></html>`,
		html.EscapeString("联单 "+manifest.Number), html.EscapeString(manifest.RenderDocument()))
}

func (s *Server) confirmManifestFlow(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.loadManifest(w, r)
	if !ok {
		return
	}
	var input manifestFlowRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.plant.ConfirmManifestFlow(manifest.ID, input.PumpID, input.Flow)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) completeManifest(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.loadManifest(w, r)
	if !ok {
		return
	}
	updated, err := s.plant.CompleteSludgeManifest(manifest.ID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) failManifest(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.loadManifest(w, r)
	if !ok {
		return
	}
	var input manifestFailRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.plant.FailSludgeManifest(manifest.ID, input.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
