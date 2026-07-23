const sourceNames = {
  codex: "Codex",
  claude: "Claude Code",
  kimi: "Kimi Code"
};

const statusText = {
  completed: "任务已完成",
  failed: "任务执行失败",
  attention: "正在等待你的处理",
  info: "有一条新消息"
};

const statusIcon = {
  completed: "✅",
  failed: "❌",
  attention: "⚠️",
  info: "🔔"
};

export function renderText(notification, config) {
  const lines = [
    `${statusIcon[notification.status]} AgentBell · ${sourceNames[notification.source]}`,
    statusText[notification.status],
    `事件：${notification.event}`
  ];

  if (notification.project) {
    lines.push(`项目：${notification.project}`);
  }

  if (config.notifications?.includeSummary && notification.summary) {
    lines.push(`摘要：${notification.summary}`);
  }

  return lines.join("\n");
}

