package adapter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

type kimiAuditBlock struct {
	index      int
	start      int
	event      string
	command    string
	commandSet bool
	eventSet   bool
}

func (adapter *KimiAdapter) AuditHooks() (hookaudit.Report, error) {
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
	source := adapter.configPath()
	raw, err := readHookAuditFile(source)
	if err != nil {
		return hookaudit.Report{}, err
	}
	content := string(raw)
	if hasInlineHooksConflict(content) {
		return hookaudit.Report{}, errors.New(
			"unsafe Kimi Hook structure: top-level inline hooks value",
		)
	}
	region, foundRegion, err := findKimiRegion(content)
	if err != nil {
		return hookaudit.Report{}, err
	}
	blocks, err := parseKimiAuditBlocks(content)
	if err != nil {
		return hookaudit.Report{}, err
	}

	request := hookaudit.Request{
		Desired: make([]hookaudit.DesiredHook, 0, len(kimiHookEvents)),
		Entries: make([]hookaudit.Entry, 0, len(blocks)),
	}
	for _, eventName := range kimiHookEvents {
		request.Desired = append(request.Desired, hookaudit.DesiredHook{
			Adapter:    kimiAdapterID,
			Event:      eventName,
			SourceFile: source,
			Path:       []string{"hooks", "append"},
			Invocation: desiredInvocation,
		})
	}

	managedBlocks := make([]kimiAuditBlock, 0)
	for _, block := range blocks {
		if !block.eventSet || !block.commandSet {
			return hookaudit.Report{}, fmt.Errorf(
				"unsafe Kimi Hook table %d: string event and command are required",
				block.index,
			)
		}
		if !isKimiAuditEvent(block.event) {
			continue
		}
		path := []string{"hooks", strconv.Itoa(block.index)}
		candidate := hookaudit.Entry{
			Adapter:    kimiAdapterID,
			Event:      block.event,
			SourceFile: source,
			Path:       path,
		}
		value, parseErr := parseAuditShellInvocation(block.command)
		if parseErr != nil {
			candidate.UnsafeReason = parseErr.Error()
		} else {
			candidate.Invocation = value
		}
		request.Entries = append(request.Entries, candidate)
		if foundRegion && block.start >= region.start && block.start < region.end {
			managedBlocks = append(managedBlocks, block)
		}
	}

	if foundRegion {
		owned, err := validateManagedKimiRegion(region, managedBlocks)
		if err != nil {
			return hookaudit.Report{}, err
		}
		for _, eventName := range kimiHookEvents {
			request.OwnedLegacy = append(request.OwnedLegacy, hookaudit.OwnedHook{
				Adapter:    kimiAdapterID,
				Event:      eventName,
				Invocation: owned,
				Proof:      hookaudit.ProofManagedRegion,
			})
		}
	}
	if receipt, ok := adapter.auditReceipt(); ok {
		if owned, parseErr := parseAuditShellInvocation(receipt.Command); parseErr == nil {
			for _, eventName := range kimiHookEvents {
				request.OwnedLegacy = append(request.OwnedLegacy, hookaudit.OwnedHook{
					Adapter:    kimiAdapterID,
					Event:      eventName,
					Invocation: owned,
					Proof:      hookaudit.ProofReceipt,
				})
			}
		}
	}
	return hookaudit.Audit(request)
}

func parseKimiAuditBlocks(content string) ([]kimiAuditBlock, error) {
	lines := strings.SplitAfter(content, "\n")
	blocks := make([]kimiAuditBlock, 0)
	var current *kimiAuditBlock
	offset := 0
	finish := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}
	for _, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\n"))
		if line == "[[hooks]]" {
			finish()
			current = &kimiAuditBlock{index: len(blocks), start: offset}
			offset += len(rawLine)
			continue
		}
		if current != nil && strings.HasPrefix(line, "[") {
			finish()
			offset += len(rawLine)
			continue
		}
		if current != nil && line != "" && !strings.HasPrefix(line, "#") {
			name, rawValue, ok := strings.Cut(line, "=")
			if ok {
				switch strings.TrimSpace(name) {
				case "event":
					value, err := parseAuditTOMLString(strings.TrimSpace(rawValue))
					if err != nil {
						return nil, fmt.Errorf(
							"unsafe Kimi Hook event in table %d: %w",
							current.index,
							err,
						)
					}
					current.event = value
					current.eventSet = true
				case "command":
					value, err := parseAuditTOMLString(strings.TrimSpace(rawValue))
					if err != nil {
						return nil, fmt.Errorf(
							"unsafe Kimi Hook command in table %d: %w",
							current.index,
							err,
						)
					}
					current.command = value
					current.commandSet = true
				}
			}
		}
		offset += len(rawLine)
	}
	finish()
	return blocks, nil
}

func parseAuditTOMLString(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("value must be a TOML basic string")
	}
	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func validateManagedKimiRegion(
	region kimiRegion,
	blocks []kimiAuditBlock,
) (hookaudit.Invocation, error) {
	if len(blocks) != len(kimiHookEvents) {
		return hookaudit.Invocation{}, errors.New(
			"unsafe Kimi managed region: Hook table count is incomplete",
		)
	}
	events := make(map[string]bool, len(blocks))
	command := ""
	for _, block := range blocks {
		if !block.eventSet || !block.commandSet {
			return hookaudit.Invocation{}, errors.New(
				"unsafe Kimi managed region: event or command is missing",
			)
		}
		if events[block.event] || !isKimiAuditEvent(block.event) {
			return hookaudit.Invocation{}, errors.New(
				"unsafe Kimi managed region: event set is invalid",
			)
		}
		events[block.event] = true
		if command == "" {
			command = block.command
		} else if command != block.command {
			return hookaudit.Invocation{}, errors.New(
				"unsafe Kimi managed region: commands differ",
			)
		}
	}
	if region.hash != hashBytes([]byte(command)) {
		return hookaudit.Invocation{}, errors.New(
			"unsafe Kimi managed region: command hash does not match",
		)
	}
	owned, err := parseAuditShellInvocation(command)
	if err != nil {
		return hookaudit.Invocation{}, fmt.Errorf(
			"unsafe Kimi managed region command: %w",
			err,
		)
	}
	return owned, nil
}

func isKimiAuditEvent(eventName string) bool {
	for _, expected := range kimiHookEvents {
		if eventName == expected {
			return true
		}
	}
	return false
}

func (adapter *KimiAdapter) auditReceipt() (kimiReceipt, bool) {
	var receipt kimiReceipt
	if !readAuditReceipt(adapter.receiptPath(), &receipt) ||
		receipt.Adapter != kimiAdapterID ||
		receipt.HookPath != adapter.configPath() ||
		receipt.Command == "" ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil {
		return kimiReceipt{}, false
	}
	return receipt, true
}
