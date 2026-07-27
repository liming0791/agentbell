package adapter

import (
	"fmt"
	"strings"
)

type shellHookSpec struct {
	Event   string
	Matcher string
}

func mergeShellHooks(
	root map[string]any,
	specs []shellHookSpec,
	command string,
	requireVersion bool,
) (bool, error) {
	changed := false
	if requireVersion {
		value, exists := root["version"]
		if exists && value != float64(1) {
			return false, errorsForHookVersion(value)
		}
		if !exists {
			root["version"] = float64(1)
			changed = true
		}
	}
	hooks, err := objectField(root, "hooks", true)
	if err != nil {
		return false, err
	}
	for _, spec := range specs {
		if hasShellHook(root, spec, command) {
			continue
		}
		removed, err := removeShellHookFromEvent(
			hooks,
			spec.Event,
			func(handler map[string]any) bool {
				return matchesShellHookHandler(handler, command)
			},
		)
		if err != nil {
			return false, err
		}
		changed = changed || removed
		groups, err := arrayField(hooks, spec.Event, true)
		if err != nil {
			return false, err
		}
		group := map[string]any{
			"hooks": []any{shellHookHandler(command)},
		}
		if spec.Matcher != "" {
			group["matcher"] = spec.Matcher
		}
		hooks[spec.Event] = append(groups, group)
		changed = true
	}
	return changed, nil
}

func removeShellHooks(
	root map[string]any,
	specs []shellHookSpec,
	command string,
) (bool, error) {
	return removeShellHooksWhere(
		root,
		specs,
		func(handler map[string]any) bool {
			return matchesShellHookHandler(handler, command)
		},
	)
}

func removeManagedShellHooks(
	root map[string]any,
	specs []shellHookSpec,
	adapterID string,
	surface string,
	keepCommand string,
) (bool, error) {
	return removeShellHooksWhere(
		root,
		specs,
		func(handler map[string]any) bool {
			return matchesManagedShellHookHandler(
				handler,
				adapterID,
				surface,
				keepCommand,
			)
		},
	)
}

func removeShellHooksWhere(
	root map[string]any,
	specs []shellHookSpec,
	matches func(map[string]any) bool,
) (bool, error) {
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		return false, err
	}
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, spec := range specs {
		removed, err := removeShellHookFromEvent(hooks, spec.Event, matches)
		if err != nil {
			return false, err
		}
		changed = changed || removed
	}
	return changed, nil
}

func removeShellHookFromEvent(
	hooks map[string]any,
	eventName string,
	matches func(map[string]any) bool,
) (bool, error) {
	groups, err := arrayField(hooks, eventName, false)
	if err != nil {
		return false, err
	}
	if groups == nil {
		return false, nil
	}
	changed := false
	filteredGroups := make([]any, 0, len(groups))
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			return false, fmt.Errorf("hooks.%s contains a non-object matcher group", eventName)
		}
		handlers, err := arrayField(group, "hooks", false)
		if err != nil {
			return false, err
		}
		filteredHandlers := make([]any, 0, len(handlers))
		for _, handlerValue := range handlers {
			handler, ok := handlerValue.(map[string]any)
			if ok && matches(handler) {
				changed = true
				continue
			}
			filteredHandlers = append(filteredHandlers, handlerValue)
		}
		if len(filteredHandlers) == 0 && len(handlers) > 0 {
			continue
		}
		group["hooks"] = filteredHandlers
		filteredGroups = append(filteredGroups, group)
	}
	if len(filteredGroups) == 0 {
		delete(hooks, eventName)
	} else {
		hooks[eventName] = filteredGroups
	}
	return changed, nil
}

func hasShellHook(root map[string]any, spec shellHookSpec, command string) bool {
	hooks, err := objectField(root, "hooks", false)
	if err != nil || hooks == nil {
		return false
	}
	groups, err := arrayField(hooks, spec.Event, false)
	if err != nil {
		return false
	}
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		if spec.Matcher != "" && group["matcher"] != spec.Matcher {
			continue
		}
		handlers, _ := arrayField(group, "hooks", false)
		for _, handlerValue := range handlers {
			handler, ok := handlerValue.(map[string]any)
			if ok && matchesShellHookHandler(handler, command) {
				return true
			}
		}
	}
	return false
}

func shellHookHandler(command string) map[string]any {
	return map[string]any{
		"type":    "command",
		"command": command,
		"timeout": float64(5),
	}
}

func matchesShellHookHandler(handler map[string]any, command string) bool {
	return handler["type"] == "command" && handler["command"] == command
}

func matchesManagedShellHookHandler(
	handler map[string]any,
	adapterID string,
	surface string,
	keepCommand string,
) bool {
	if handler["type"] != "command" {
		return false
	}
	command, ok := handler["command"].(string)
	if !ok || command == keepCommand {
		return false
	}
	arguments := " emit --adapter " + adapterID +
		" --surface " + surface +
		" --runtime host --stdin --fail-open"
	return strings.HasSuffix(command, arguments)
}

func errorsForHookVersion(value any) error {
	return fmt.Errorf("unsupported hooks.json version %v; expected version 1", value)
}
