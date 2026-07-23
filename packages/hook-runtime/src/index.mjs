import { loadConfig } from "./config.mjs";
import { normalizeEvent } from "./normalize.mjs";
import { renderText } from "./render.mjs";
import { sendWithLarkCli } from "./transport/lark-cli.mjs";

function selectChannel(config) {
  return config.channels?.find((channel) => channel.id === config.defaultChannel);
}

function eventEnabled(config, event) {
  const enabledEvents = config.notifications?.events;
  return !Array.isArray(enabledEvents) || enabledEvents.includes(event);
}

export async function handleHook({ source, rawInput, dryRun = false }) {
  const notification = normalizeEvent(source, rawInput);
  const loaded = await loadConfig();

  if (!loaded.value) {
    return {
      sent: false,
      reason: "config-missing",
      configPath: loaded.path,
      notification
    };
  }

  if (!eventEnabled(loaded.value, notification.event)) {
    return {
      sent: false,
      reason: "event-disabled",
      notification
    };
  }

  const channel = selectChannel(loaded.value);
  if (!channel) {
    return {
      sent: false,
      reason: "channel-missing",
      notification
    };
  }

  const text = renderText(notification, loaded.value);
  if (!dryRun) {
    await sendWithLarkCli(channel, text);
  }

  return {
    sent: !dryRun,
    dryRun,
    channel: channel.id,
    text,
    notification
  };
}

