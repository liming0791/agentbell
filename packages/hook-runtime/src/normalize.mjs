import path from "node:path";

const supportedSources = new Set(["codex", "claude", "kimi"]);

function parseInput(rawInput) {
  if (!rawInput.trim()) {
    return {};
  }

  try {
    return JSON.parse(rawInput);
  } catch {
    return {
      message: rawInput.trim()
    };
  }
}

function compact(value, maxLength = 300) {
  if (typeof value !== "string") {
    return null;
  }

  const normalized = value.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return null;
  }

  return normalized.slice(0, maxLength);
}

function statusFor(event) {
  if (/failure|error/i.test(event)) {
    return "failed";
  }
  if (/permission|approval/i.test(event)) {
    return "attention";
  }
  if (/stop|completed/i.test(event)) {
    return "completed";
  }
  return "info";
}

export function normalizeEvent(source, rawInput) {
  if (!supportedSources.has(source)) {
    throw new Error(`Unsupported hook source: ${source ?? "<missing>"}`);
  }

  const payload = parseInput(rawInput);
  const event = payload.hook_event_name ?? payload.event ?? "Unknown";
  const cwd = typeof payload.cwd === "string" ? payload.cwd : null;

  return {
    version: "1",
    source,
    event,
    status: statusFor(event),
    occurredAt: new Date().toISOString(),
    sessionId: payload.session_id ?? payload.thread_id ?? null,
    project: cwd ? path.basename(cwd) : null,
    cwd,
    summary: compact(
      payload.last_assistant_message ??
      payload.message ??
      payload.reason ??
      null
    )
  };
}

