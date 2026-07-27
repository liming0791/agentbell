package adapter

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

type jsonAuditHandler func(map[string]any) (hookaudit.Invocation, string)

func (adapter *CodexAdapter) AuditHooks() (hookaudit.Report, error) {
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return hookaudit.Report{}, err
	}
	desiredInvocation, err := stableAuditInvocation(
		invocation,
		hookaudit.FormShell,
	)
	if err != nil {
		return hookaudit.Report{}, err
	}
	source := adapter.hookPath()
	root, err := readHookAuditJSONObject(source)
	if err != nil {
		return hookaudit.Report{}, err
	}
	commandField := "command"
	if runtime.GOOS == "windows" {
		commandField = "commandWindows"
	}
	request, err := normalizeJSONAuditRequest(
		root,
		codexAdapterID,
		source,
		[]string{"Stop"},
		desiredInvocation,
		func(handler map[string]any) (hookaudit.Invocation, string) {
			if handler["type"] != "command" {
				return hookaudit.Invocation{}, "Codex Hook handler type must be command"
			}
			for _, field := range []string{"command", "commandWindows"} {
				command, ok := handler[field].(string)
				if !ok || command == "" {
					return hookaudit.Invocation{},
						"Codex Hook " + field + " must be a string"
				}
				if _, err := parseAuditShellInvocation(command); err != nil {
					return hookaudit.Invocation{}, err.Error()
				}
			}
			command, ok := handler[commandField].(string)
			if !ok {
				return hookaudit.Invocation{}, "Codex Hook command is unavailable"
			}
			value, err := parseAuditShellInvocation(command)
			if err != nil {
				return hookaudit.Invocation{}, err.Error()
			}
			return value, ""
		},
	)
	if err != nil {
		return hookaudit.Report{}, err
	}
	if receipt, ok := adapter.auditReceipt(); ok {
		command := receipt.Command
		if runtime.GOOS == "windows" {
			command = receipt.CommandWindows
		}
		if owned, err := parseAuditShellInvocation(command); err == nil {
			request.OwnedLegacy = append(request.OwnedLegacy, hookaudit.OwnedHook{
				Adapter:    codexAdapterID,
				Event:      "Stop",
				Invocation: owned,
				Proof:      hookaudit.ProofReceipt,
			})
		}
	}
	return hookaudit.Audit(request)
}

func (adapter *ClaudeAdapter) AuditHooks() (hookaudit.Report, error) {
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return hookaudit.Report{}, err
	}
	desiredInvocation, err := stableAuditInvocation(
		invocation,
		hookaudit.FormExec,
	)
	if err != nil {
		return hookaudit.Report{}, err
	}
	source := adapter.settingsPath()
	root, err := readHookAuditJSONObject(source)
	if err != nil {
		return hookaudit.Report{}, err
	}
	request, err := normalizeJSONAuditRequest(
		root,
		claudeAdapterID,
		source,
		claudeHookEvents,
		desiredInvocation,
		func(handler map[string]any) (hookaudit.Invocation, string) {
			if handler["type"] != "command" {
				return hookaudit.Invocation{}, "Claude Hook handler type must be command"
			}
			command, ok := handler["command"].(string)
			if !ok || command == "" {
				return hookaudit.Invocation{}, "Claude Hook command must be a string"
			}
			rawArgs, ok := handler["args"].([]any)
			if !ok {
				return hookaudit.Invocation{}, "Claude Hook args must be an array"
			}
			args := make([]string, 0, len(rawArgs))
			for _, raw := range rawArgs {
				value, ok := raw.(string)
				if !ok {
					return hookaudit.Invocation{}, "Claude Hook args must contain only strings"
				}
				args = append(args, value)
			}
			return hookaudit.Invocation{
				Form:       hookaudit.FormExec,
				Executable: command,
				Args:       args,
			}, ""
		},
	)
	if err != nil {
		return hookaudit.Report{}, err
	}
	if receipt, ok := adapter.auditReceipt(); ok {
		request.OwnedLegacy = append(request.OwnedLegacy, hookaudit.OwnedHook{
			Adapter: claudeAdapterID,
			Event:   "",
			Invocation: hookaudit.Invocation{
				Form:       hookaudit.FormExec,
				Executable: receipt.Command,
				Args:       append([]string(nil), receipt.Args...),
			},
			Proof: hookaudit.ProofManagedReceipt,
		})
		owned := request.OwnedLegacy[len(request.OwnedLegacy)-1]
		request.OwnedLegacy = request.OwnedLegacy[:len(request.OwnedLegacy)-1]
		for _, eventName := range claudeHookEvents {
			owned.Event = eventName
			request.OwnedLegacy = append(request.OwnedLegacy, owned)
		}
	}
	return hookaudit.Audit(request)
}

func normalizeJSONAuditRequest(
	root map[string]any,
	adapterID,
	source string,
	events []string,
	desiredInvocation hookaudit.Invocation,
	parse jsonAuditHandler,
) (hookaudit.Request, error) {
	request := hookaudit.Request{
		Desired: make([]hookaudit.DesiredHook, 0, len(events)),
		Entries: make([]hookaudit.Entry, 0),
	}
	for _, eventName := range events {
		request.Desired = append(request.Desired, hookaudit.DesiredHook{
			Adapter:    adapterID,
			Event:      eventName,
			SourceFile: source,
			Path: []string{
				"hooks", eventName, "append", "hooks", "append",
			},
			Invocation: desiredInvocation,
		})
	}

	rawHooks, exists := root["hooks"]
	if !exists {
		return request, nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return hookaudit.Request{}, errorsForAuditPath("hooks", "must be an object")
	}
	for _, eventName := range events {
		rawGroups, exists := hooks[eventName]
		if !exists {
			continue
		}
		groups, ok := rawGroups.([]any)
		if !ok {
			return hookaudit.Request{}, errorsForAuditPath(
				"hooks."+eventName,
				"must be an array",
			)
		}
		for groupIndex, rawGroup := range groups {
			groupPath := []string{"hooks", eventName, strconv.Itoa(groupIndex)}
			group, ok := rawGroup.(map[string]any)
			if !ok {
				request.Entries = append(request.Entries, unsafeJSONAuditEntry(
					adapterID,
					eventName,
					source,
					groupPath,
					"matcher group must be an object",
				))
				continue
			}
			rawHandlers, exists := group["hooks"]
			if !exists {
				request.Entries = append(request.Entries, unsafeJSONAuditEntry(
					adapterID,
					eventName,
					source,
					append(groupPath, "hooks"),
					"matcher group hooks array is missing",
				))
				continue
			}
			handlers, ok := rawHandlers.([]any)
			if !ok {
				request.Entries = append(request.Entries, unsafeJSONAuditEntry(
					adapterID,
					eventName,
					source,
					append(groupPath, "hooks"),
					"matcher group hooks must be an array",
				))
				continue
			}
			for handlerIndex, rawHandler := range handlers {
				path := append(
					append([]string(nil), groupPath...),
					"hooks",
					strconv.Itoa(handlerIndex),
				)
				handler, ok := rawHandler.(map[string]any)
				if !ok {
					request.Entries = append(request.Entries, unsafeJSONAuditEntry(
						adapterID,
						eventName,
						source,
						path,
						"Hook handler must be an object",
					))
					continue
				}
				invocation, unsafeReason := parse(handler)
				request.Entries = append(request.Entries, hookaudit.Entry{
					Adapter:      adapterID,
					Event:        eventName,
					SourceFile:   source,
					Path:         path,
					Invocation:   invocation,
					UnsafeReason: unsafeReason,
				})
			}
		}
	}
	return request, nil
}

func unsafeJSONAuditEntry(
	adapterID,
	eventName,
	source string,
	path []string,
	reason string,
) hookaudit.Entry {
	return hookaudit.Entry{
		Adapter:      adapterID,
		Event:        eventName,
		SourceFile:   source,
		Path:         append([]string(nil), path...),
		UnsafeReason: reason,
	}
}

func errorsForAuditPath(path, message string) error {
	return fmt.Errorf("unsafe Hook structure at %s: %s", path, message)
}

func (adapter *CodexAdapter) auditReceipt() (codexReceipt, bool) {
	var receipt codexReceipt
	if !readAuditReceipt(adapter.receiptPath(), &receipt) ||
		receipt.Adapter != codexAdapterID ||
		receipt.HookPath != adapter.hookPath() ||
		receipt.Command == "" ||
		receipt.CommandWindows == "" ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil {
		return codexReceipt{}, false
	}
	return receipt, true
}

func (adapter *ClaudeAdapter) auditReceipt() (claudeReceipt, bool) {
	var receipt claudeReceipt
	if !readAuditReceipt(adapter.receiptPath(), &receipt) ||
		receipt.Adapter != claudeAdapterID ||
		receipt.SettingsPath != adapter.settingsPath() ||
		receipt.Command == "" ||
		len(receipt.Args) == 0 ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil {
		return claudeReceipt{}, false
	}
	return receipt, true
}
