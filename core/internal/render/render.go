package render

import (
	"strings"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
)

var sourceNames = map[string]string{
	"codex": "Codex", "claude": "Claude Code", "opencode": "OpenCode",
	"kimi": "Kimi Code", "qoder": "Qoder", "zcode": "ZCode",
	"qoder-work": "QoderWork", "workbuddy": "WorkBuddy", "trae": "TRAE", "kimi-work": "Kimi Work",
}

var statusLines = map[string]string{
	event.StatusCompleted: "✅ 任务已完成",
	event.StatusFailed:    "❌ 任务执行失败",
	event.StatusAttention: "⚠️ 正在等待你的处理",
	event.StatusInfo:      "🔔 有一条新消息",
}

func Text(notification event.Notification, settings config.Config) string {
	sourceName := sourceNames[notification.Source]
	if sourceName == "" {
		sourceName = notification.Source
	}
	lines := []string{
		"AgentBell · " + sourceName,
		statusLines[notification.Status],
		"事件：" + notification.Event,
	}
	if notification.Project != "" {
		lines = append(lines, "项目："+notification.Project)
	}
	if settings.Notifications.IncludeSummary && notification.Summary != "" {
		lines = append(lines, "摘要："+notification.Summary)
	}
	return strings.Join(lines, "\n")
}
