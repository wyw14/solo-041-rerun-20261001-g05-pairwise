package sludge

import (
	"fmt"
	"strings"
	"time"
)

var statusText = map[ManifestStatus]string{
	ManifestOpen:      "待结单",
	ManifestCompleted: "已结单",
	ManifestFailed:    "已失败",
}

// RenderDocument 生成可打印的联单文本。
func (m Manifest) RenderDocument() string {
	var b strings.Builder
	line := func(label, value string) {
		fmt.Fprintf(&b, "%-12s: %s\n", label, value)
	}
	fmt.Fprintf(&b, "污泥外运联单 %s\n", m.Number)
	b.WriteString(strings.Repeat("=", 40) + "\n")
	line("联单状态", statusText[m.Status])
	line("处理线", string(m.LineID))
	line("沉淀池", m.BasinID)
	line("泵序列", strings.Join(m.PumpIDs, ", "))
	line("估计体积", fmt.Sprintf("%.2f m3", m.EstimatedVolume))
	line("接收方", m.Receiver)
	if m.Vehicle != "" {
		line("运输车辆", m.Vehicle)
	}
	line("交接时间", m.HandoverAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "%-12s:\n", "泵流量确认")
	for _, pumpID := range m.PumpIDs {
		flow := m.PumpFlows[pumpID]
		state := "未确认"
		if flow > 0 {
			state = fmt.Sprintf("%.2f m3/h", flow)
		}
		line("  "+pumpID, state)
	}
	if m.InterlockID != "" {
		line("排泥互锁", m.InterlockID)
	}
	if m.Status == ManifestFailed {
		line("失败原因", m.FailReason)
	}
	if m.ClosedAt != nil {
		line("结单时间", m.ClosedAt.Format(time.RFC3339))
	}
	line("建单时间", m.CreatedAt.Format(time.RFC3339))
	b.WriteString("\n发运方签字: ______________  接收方签字: ______________\n")
	return b.String()
}
