package api

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sludge"
)

// ErrManifestNotFound is returned when a consignment note does not exist.
var ErrManifestNotFound = errors.New("manifest is not found")

type manifestRequest struct {
	LineID          model.LineID `json:"line_id"`
	EstimatedVolume float64      `json:"estimated_volume_m3"`
	ReceiverCompany string       `json:"receiver_company"`
	ReceiverPlate   string       `json:"receiver_plate"`
	ReceiverDriver  string       `json:"receiver_driver"`
	ReceiverContact string       `json:"receiver_contact"`
	HandoverAt      *time.Time   `json:"handover_at"`
}

type manifestFailRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) listManifests(w http.ResponseWriter, r *http.Request) {
	lineID, _ := model.ParseLineID(r.URL.Query().Get("line_id"))
	filter := sludge.ManifestFilter{
		LineID:   lineID,
		Status:   sludge.ManifestStatus(r.URL.Query().Get("status")),
		Receiver: r.URL.Query().Get("receiver"),
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": s.plant.Manifests(filter)})
}

func (s *Server) createManifest(w http.ResponseWriter, r *http.Request) {
	var input manifestRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	lineID, err := model.ParseLineID(string(input.LineID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var handoverAt time.Time
	if input.HandoverAt != nil {
		handoverAt = input.HandoverAt.UTC()
	}
	manifest, err := s.plant.OpenManifest(lineID, input.EstimatedVolume, sludge.Receiver{
		Company: input.ReceiverCompany, Plate: input.ReceiverPlate,
		Driver: input.ReceiverDriver, Contact: input.ReceiverContact,
	}, handoverAt)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, manifest)
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "manifestID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manifest, ok := s.plant.Manifest(id)
	if !ok {
		writeError(w, http.StatusNotFound, ErrManifestNotFound)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) completeManifest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "manifestID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manifest, err := s.plant.CompleteManifest(id)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) failManifest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "manifestID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input manifestFailRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manifest, compensation, err := s.plant.FailManifest(id, input.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"manifest": manifest, "compensation": compensation})
}

var manifestPrintTemplate = template.Must(template.New("manifest").Funcs(template.FuncMap{
	"statusLabel": func(status sludge.ManifestStatus) string {
		switch status {
		case sludge.ManifestCompleted:
			return "已结单"
		case sludge.ManifestFailed:
			return "失败"
		default:
			return "待结单"
		}
	},
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format("2006-01-02 15:04:05 MST")
	},
	"fmtTimePtr": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.UTC().Format("2006-01-02 15:04:05 MST")
	},
	"manifestNumber": func(n uint64) string { return fmt.Sprintf("WN-%06d", n) },
}).Parse(`<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>污泥外运联单 {{manifestNumber .Number}}</title>
<style>
body{font-family:"Segoe UI","Microsoft YaHei",sans-serif;color:#172027;background:#f3f5f6;margin:0;padding:24px}
.sheet{max-width:820px;margin:0 auto;background:#fff;border:1px solid #c9d3d7;border-radius:6px;padding:34px 40px}
.head{display:flex;justify-content:space-between;align-items:flex-start;border-bottom:3px solid #206c63;padding-bottom:14px;margin-bottom:20px}
h1{font-size:22px;margin:0 0 4px}.sub{color:#607179;font-size:13px}
.badge{border-radius:4px;padding:6px 14px;font-weight:700;font-size:14px;color:#fff;background:#b07d24}
.badge.completed{background:#206c63}.badge.failed{background:#a63e37}
table{width:100%;border-collapse:collapse;font-size:14px}
th,td{border:1px solid #d4dde0;padding:10px 12px;text-align:left;vertical-align:top}
th{background:#eef4f3;width:170px;color:#40535b;font-weight:650}
.section-title{margin:22px 0 8px;font-size:14px;font-weight:700;color:#206c63}
.sign{display:flex;gap:40px;margin-top:34px}.sign div{flex:1;border-top:1px solid #4a5b62;padding-top:8px;color:#607179;font-size:13px}
.actions{max-width:820px;margin:14px auto 0;display:flex;gap:10px;justify-content:flex-end}
button{border:0;border-radius:4px;padding:9px 18px;background:#206c63;color:#fff;font-weight:650;cursor:pointer}
@media print{body{background:#fff;padding:0}.sheet{border:0}.actions{display:none}}
</style></head>
<body>
<div class="actions"><button onclick="window.print()">打印联单</button></div>
<div class="sheet">
  <div class="head"><div><h1>污泥外运联单</h1><div class="sub">联单编号：{{manifestNumber .Number}}</div></div>
  <span class="badge {{if eq (printf "%s" .Status) "completed"}}completed{{else if eq (printf "%s" .Status) "failed"}}failed{{end}}">{{statusLabel .Status}}</span></div>
  <table>
    <tr><th>处理线</th><td>{{.LineID}}</td><th>沉淀池</th><td>{{.BasinID}}</td></tr>
    <tr><th>泵序列</th><td>#{{.SequenceNumber}}（{{.PumpID}}）</td><th>世代</th><td>{{.Generation}}</td></tr>
    <tr><th>估计体积</th><td>{{.EstimatedVolumeM3}} m³</td><th>确认流量</th><td>{{if .ConfirmedFlowM3H}}{{.ConfirmedFlowM3H}} m³/h{{else}}—{{end}}</td></tr>
    <tr><th>计划交接时间</th><td colspan="3">{{fmtTime .PlannedHandoverAt}}</td></tr>
  </table>
  <div class="section-title">接收方</div>
  <table>
    <tr><th>承运单位</th><td>{{.Receiver.Company}}</td><th>车牌号</th><td>{{.Receiver.Plate}}</td></tr>
    <tr><th>驾驶员</th><td>{{.Receiver.Driver}}</td><th>联系方式</th><td>{{if .Receiver.Contact}}{{.Receiver.Contact}}{{else}}—{{end}}</td></tr>
  </table>
  <div class="section-title">处置记录</div>
  <table>
    <tr><th>开单时间</th><td>{{fmtTime .CreatedAt}}</td><th>结单时间</th><td>{{fmtTimePtr .ClosedAt}}</td></tr>
    {{if .FailureReason}}<tr><th>失败原因</th><td colspan="3" style="color:#a63e37">{{.FailureReason}}</td></tr>{{end}}
    <tr><th>补偿动作</th><td colspan="3">{{if .CompensationCompleted}}已执行停泵补偿{{else if .FailureReason}}停泵补偿待执行{{else}}—{{end}}</td></tr>
  </table>
  <div class="sign"><div>生产运行签章 / 日期</div><div>接收方签章 / 日期</div></div>
</div>
</body></html>`))

func (s *Server) printManifest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "manifestID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	manifest, ok := s.plant.Manifest(id)
	if !ok {
		http.Error(w, ErrManifestNotFound.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := manifestPrintTemplate.Execute(w, manifest); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
