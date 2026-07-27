// Package hookaudit classifies already-parsed hook entries and produces a
// deterministic, non-executing reconcile plan. It deliberately does not read
// vendor configuration files or construct shell command strings.
package hookaudit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const ReportVersion = 1

type Form string

const (
	FormExec  Form = "exec"
	FormShell Form = "shell"
)

type Proof string

const (
	ProofReceipt        Proof = "receipt"
	ProofManagedReceipt Proof = "managed-receipt"
	ProofManagedRegion  Proof = "managed-region"
)

type Kind string

const (
	KindCurrentStableBridge Kind = "current-stable-bridge"
	KindOwnedLegacy         Kind = "owned-legacy-agentbell"
	KindOwnedDuplicate      Kind = "owned-duplicate-agentbell"
	KindExternalSameEvent   Kind = "external-same-event"
	KindUnsafeStructure     Kind = "unsafe-structure"
	KindMissingStableBridge Kind = "missing-stable-bridge"
)

type Operation string

const (
	OperationAdd    Operation = "add"
	OperationRemove Operation = "remove"
)

type Invocation struct {
	Form       Form     `json:"form"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

type Entry struct {
	Adapter      string     `json:"adapter"`
	Event        string     `json:"event"`
	SourceFile   string     `json:"sourceFile"`
	Path         []string   `json:"path"`
	Invocation   Invocation `json:"invocation"`
	UnsafeReason string     `json:"unsafeReason,omitempty"`
}

type DesiredHook struct {
	Adapter    string     `json:"adapter"`
	Event      string     `json:"event"`
	SourceFile string     `json:"sourceFile"`
	Path       []string   `json:"path"`
	Invocation Invocation `json:"invocation"`
}

type OwnedHook struct {
	Adapter    string     `json:"adapter"`
	Event      string     `json:"event"`
	Invocation Invocation `json:"invocation"`
	Proof      Proof      `json:"proof"`
}

type Request struct {
	Desired     []DesiredHook `json:"desired"`
	OwnedLegacy []OwnedHook   `json:"ownedLegacy"`
	Entries     []Entry       `json:"entries"`
}

type Finding struct {
	Kind       Kind     `json:"kind"`
	Adapter    string   `json:"adapter"`
	Event      string   `json:"event"`
	SourceFile string   `json:"sourceFile"`
	Path       []string `json:"path"`
	Form       Form     `json:"form,omitempty"`
	Executable string   `json:"executable,omitempty"`
	Args       []string `json:"args"`
	Detail     string   `json:"detail,omitempty"`
}

type Summary struct {
	CurrentStableBridge int `json:"currentStableBridge"`
	OwnedLegacy         int `json:"ownedLegacy"`
	OwnedDuplicate      int `json:"ownedDuplicate"`
	ExternalSameEvent   int `json:"externalSameEvent"`
	UnsafeStructure     int `json:"unsafeStructure"`
	MissingStableBridge int `json:"missingStableBridge"`
}

type Action struct {
	Operation  Operation `json:"operation"`
	Reason     Kind      `json:"reason"`
	Adapter    string    `json:"adapter"`
	Event      string    `json:"event"`
	SourceFile string    `json:"sourceFile"`
	Path       []string  `json:"path"`
	Form       Form      `json:"form"`
	Executable string    `json:"executable"`
	Args       []string  `json:"args"`
}

type ReconcilePlan struct {
	Blocked bool     `json:"blocked"`
	Actions []Action `json:"actions"`
}

type Report struct {
	Version  int           `json:"version"`
	Summary  Summary       `json:"summary"`
	Findings []Finding     `json:"findings"`
	Plan     ReconcilePlan `json:"plan"`
}

func Audit(request Request) (Report, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Version:  ReportVersion,
		Findings: make([]Finding, 0),
		Plan: ReconcilePlan{
			Actions: make([]Action, 0),
		},
	}

	entriesByTarget := make(map[string][]Entry)
	for _, candidate := range normalized.Entries {
		key := targetKey(candidate.Adapter, candidate.Event)
		entriesByTarget[key] = append(entriesByTarget[key], candidate)
	}
	legacyByTarget := make(map[string][]OwnedHook)
	for _, owned := range normalized.OwnedLegacy {
		key := targetKey(owned.Adapter, owned.Event)
		legacyByTarget[key] = append(legacyByTarget[key], owned)
	}

	for _, target := range normalized.Desired {
		key := targetKey(target.Adapter, target.Event)
		entries := entriesByTarget[key]
		owned := legacyByTarget[key]
		unsafeLocators := duplicateLocators(entries)
		targetBlocked := false
		currentCount := 0
		legacyCounts := make(map[string]int)
		classified := make([]Finding, 0, len(entries))
		removals := make([]Action, 0)

		for _, candidate := range entries {
			unsafeReason := candidate.UnsafeReason
			if unsafeReason == "" {
				unsafeReason = validateEntry(candidate)
			}
			if unsafeLocators[locatorKey(candidate)] {
				unsafeReason = "normalized hook locator is duplicated"
			}
			if unsafeReason != "" {
				targetBlocked = true
				classified = append(classified, findingFor(
					KindUnsafeStructure,
					candidate,
					unsafeReason,
				))
				continue
			}

			switch {
			case equalInvocation(candidate.Invocation, target.Invocation):
				currentCount++
				if currentCount == 1 {
					classified = append(classified, findingFor(
						KindCurrentStableBridge,
						candidate,
						"",
					))
					continue
				}
				classified = append(classified, findingFor(
					KindOwnedDuplicate,
					candidate,
					"duplicate of the current stable bridge hook",
				))
				removals = append(removals, actionFor(
					OperationRemove,
					KindOwnedDuplicate,
					candidate,
				))
			default:
				legacy, matched := matchingOwned(candidate, owned)
				if !matched {
					classified = append(classified, findingFor(
						KindExternalSameEvent,
						candidate,
						"external hook is report-only",
					))
					continue
				}
				invocationKey := invocationKey(legacy.Invocation)
				legacyCounts[invocationKey]++
				kind := KindOwnedLegacy
				detail := "exact invocation is proven by " + string(legacy.Proof)
				if legacyCounts[invocationKey] > 1 {
					kind = KindOwnedDuplicate
					detail = "duplicate of an exactly proven legacy AgentBell hook"
				}
				classified = append(classified, findingFor(kind, candidate, detail))
				removals = append(removals, actionFor(
					OperationRemove,
					kind,
					candidate,
				))
			}
		}

		if currentCount == 0 {
			missing := findingForDesired(
				KindMissingStableBridge,
				target,
				"current stable bridge hook is absent",
			)
			report.Findings = append(report.Findings, missing)
			if !targetBlocked {
				report.Plan.Actions = append(
					report.Plan.Actions,
					actionForDesired(OperationAdd, KindMissingStableBridge, target),
				)
			}
		}
		report.Findings = append(report.Findings, classified...)
		if targetBlocked {
			report.Plan.Blocked = true
		} else {
			report.Plan.Actions = append(report.Plan.Actions, removals...)
		}
	}

	for _, finding := range report.Findings {
		incrementSummary(&report.Summary, finding.Kind)
	}
	sortActions(report.Plan.Actions)
	return report, nil
}

func AuditJSON(request Request) ([]byte, error) {
	report, err := Audit(request)
	if err != nil {
		return nil, err
	}
	value, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(value, '\n'), nil
}

func normalizeRequest(request Request) (Request, error) {
	if len(request.Desired) == 0 {
		return Request{}, errors.New("at least one desired stable bridge hook is required")
	}
	result := Request{
		Desired:     append([]DesiredHook(nil), request.Desired...),
		OwnedLegacy: append([]OwnedHook(nil), request.OwnedLegacy...),
		Entries:     append([]Entry(nil), request.Entries...),
	}
	desiredTargets := make(map[string]bool, len(result.Desired))
	for index := range result.Desired {
		candidate := &result.Desired[index]
		normalizeDesired(candidate)
		if err := validateIdentity(
			candidate.Adapter,
			candidate.Event,
			candidate.SourceFile,
			candidate.Path,
		); err != nil {
			return Request{}, fmt.Errorf("desired[%d]: %w", index, err)
		}
		if err := validateInvocation(candidate.Invocation); err != nil {
			return Request{}, fmt.Errorf("desired[%d]: %w", index, err)
		}
		key := targetKey(candidate.Adapter, candidate.Event)
		if desiredTargets[key] {
			return Request{}, fmt.Errorf(
				"duplicate desired hook for %s/%s",
				candidate.Adapter,
				candidate.Event,
			)
		}
		desiredTargets[key] = true
	}
	for index := range result.OwnedLegacy {
		candidate := &result.OwnedLegacy[index]
		normalizeOwned(candidate)
		if !validProof(candidate.Proof) {
			return Request{}, fmt.Errorf(
				"ownedLegacy[%d]: unsupported ownership proof %q",
				index,
				candidate.Proof,
			)
		}
		if !desiredTargets[targetKey(candidate.Adapter, candidate.Event)] {
			return Request{}, fmt.Errorf(
				"ownedLegacy[%d]: target has no desired stable hook",
				index,
			)
		}
		if err := validateInvocation(candidate.Invocation); err != nil {
			return Request{}, fmt.Errorf("ownedLegacy[%d]: %w", index, err)
		}
	}
	filteredEntries := make([]Entry, 0, len(result.Entries))
	for index := range result.Entries {
		candidate := &result.Entries[index]
		normalizeEntry(candidate)
		if candidate.Adapter == "" || candidate.Event == "" {
			return Request{}, fmt.Errorf(
				"entries[%d]: adapter and event are required",
				index,
			)
		}
		if desiredTargets[targetKey(candidate.Adapter, candidate.Event)] {
			filteredEntries = append(filteredEntries, *candidate)
		}
	}
	result.Entries = filteredEntries
	sortDesired(result.Desired)
	sortOwned(result.OwnedLegacy)
	sortEntries(result.Entries)
	return result, nil
}

func validateIdentity(
	adapter,
	event,
	source string,
	path []string,
) error {
	if adapter == "" || event == "" {
		return errors.New("adapter and event are required")
	}
	if source == "" {
		return errors.New("sourceFile is required")
	}
	return validatePath(path)
}

func validateEntry(candidate Entry) string {
	if err := validateIdentity(
		candidate.Adapter,
		candidate.Event,
		candidate.SourceFile,
		candidate.Path,
	); err != nil {
		return err.Error()
	}
	if err := validateInvocation(candidate.Invocation); err != nil {
		return err.Error()
	}
	return ""
}

func validateInvocation(value Invocation) error {
	if value.Form != FormExec && value.Form != FormShell {
		return fmt.Errorf("unsupported hook form %q", value.Form)
	}
	if !absoluteCommand(value.Executable) {
		return errors.New("hook executable must be an absolute path")
	}
	if containsControl(value.Executable) {
		return errors.New("hook executable contains control characters")
	}
	for index, argument := range value.Args {
		if containsControl(argument) {
			return fmt.Errorf("hook argument %d contains control characters", index)
		}
	}
	return nil
}

func validatePath(path []string) error {
	if len(path) == 0 {
		return errors.New("path must contain at least one component")
	}
	for index, component := range path {
		if component == "" || containsControl(component) {
			return fmt.Errorf("path component %d is invalid", index)
		}
	}
	return nil
}

func validProof(proof Proof) bool {
	return proof == ProofReceipt ||
		proof == ProofManagedReceipt ||
		proof == ProofManagedRegion
}

func matchingOwned(candidate Entry, owned []OwnedHook) (OwnedHook, bool) {
	for _, value := range owned {
		if equalInvocation(candidate.Invocation, value.Invocation) {
			return value, true
		}
	}
	return OwnedHook{}, false
}

func equalInvocation(left, right Invocation) bool {
	if left.Form != right.Form ||
		left.Executable != right.Executable ||
		len(left.Args) != len(right.Args) {
		return false
	}
	for index := range left.Args {
		if left.Args[index] != right.Args[index] {
			return false
		}
	}
	return true
}

func duplicateLocators(entries []Entry) map[string]bool {
	counts := make(map[string]int, len(entries))
	for _, candidate := range entries {
		counts[locatorKey(candidate)]++
	}
	result := make(map[string]bool)
	for key, count := range counts {
		if count > 1 {
			result[key] = true
		}
	}
	return result
}

func findingFor(kind Kind, candidate Entry, detail string) Finding {
	return Finding{
		Kind:       kind,
		Adapter:    candidate.Adapter,
		Event:      candidate.Event,
		SourceFile: candidate.SourceFile,
		Path:       copyStrings(candidate.Path),
		Form:       candidate.Invocation.Form,
		Executable: candidate.Invocation.Executable,
		Args:       copyStrings(candidate.Invocation.Args),
		Detail:     detail,
	}
}

func findingForDesired(kind Kind, candidate DesiredHook, detail string) Finding {
	return Finding{
		Kind:       kind,
		Adapter:    candidate.Adapter,
		Event:      candidate.Event,
		SourceFile: candidate.SourceFile,
		Path:       copyStrings(candidate.Path),
		Form:       candidate.Invocation.Form,
		Executable: candidate.Invocation.Executable,
		Args:       copyStrings(candidate.Invocation.Args),
		Detail:     detail,
	}
}

func actionFor(operation Operation, reason Kind, candidate Entry) Action {
	return Action{
		Operation:  operation,
		Reason:     reason,
		Adapter:    candidate.Adapter,
		Event:      candidate.Event,
		SourceFile: candidate.SourceFile,
		Path:       copyStrings(candidate.Path),
		Form:       candidate.Invocation.Form,
		Executable: candidate.Invocation.Executable,
		Args:       copyStrings(candidate.Invocation.Args),
	}
}

func actionForDesired(
	operation Operation,
	reason Kind,
	candidate DesiredHook,
) Action {
	return Action{
		Operation:  operation,
		Reason:     reason,
		Adapter:    candidate.Adapter,
		Event:      candidate.Event,
		SourceFile: candidate.SourceFile,
		Path:       copyStrings(candidate.Path),
		Form:       candidate.Invocation.Form,
		Executable: candidate.Invocation.Executable,
		Args:       copyStrings(candidate.Invocation.Args),
	}
}

func incrementSummary(summary *Summary, kind Kind) {
	switch kind {
	case KindCurrentStableBridge:
		summary.CurrentStableBridge++
	case KindOwnedLegacy:
		summary.OwnedLegacy++
	case KindOwnedDuplicate:
		summary.OwnedDuplicate++
	case KindExternalSameEvent:
		summary.ExternalSameEvent++
	case KindUnsafeStructure:
		summary.UnsafeStructure++
	case KindMissingStableBridge:
		summary.MissingStableBridge++
	}
}

func normalizeDesired(value *DesiredHook) {
	value.Path = copyStrings(value.Path)
	value.Invocation = normalizeInvocation(value.Invocation)
}

func normalizeOwned(value *OwnedHook) {
	value.Invocation = normalizeInvocation(value.Invocation)
}

func normalizeEntry(value *Entry) {
	value.Path = copyStrings(value.Path)
	value.Invocation = normalizeInvocation(value.Invocation)
}

func normalizeInvocation(value Invocation) Invocation {
	value.Args = copyStrings(value.Args)
	return value
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func sortDesired(values []DesiredHook) {
	sort.Slice(values, func(left, right int) bool {
		return desiredSortKey(values[left]) < desiredSortKey(values[right])
	})
}

func sortEntries(values []Entry) {
	sort.Slice(values, func(left, right int) bool {
		return entrySortKey(values[left]) < entrySortKey(values[right])
	})
}

func sortOwned(values []OwnedHook) {
	sort.Slice(values, func(left, right int) bool {
		return ownedSortKey(values[left]) < ownedSortKey(values[right])
	})
}

func sortActions(values []Action) {
	sort.Slice(values, func(left, right int) bool {
		return actionSortKey(values[left]) < actionSortKey(values[right])
	})
}

func desiredSortKey(value DesiredHook) string {
	return strings.Join([]string{
		value.Adapter,
		value.Event,
		value.SourceFile,
		strings.Join(value.Path, "\x1f"),
		invocationKey(value.Invocation),
	}, "\x1e")
}

func entrySortKey(value Entry) string {
	return strings.Join([]string{
		value.Adapter,
		value.Event,
		value.SourceFile,
		strings.Join(value.Path, "\x1f"),
		invocationKey(value.Invocation),
		value.UnsafeReason,
	}, "\x1e")
}

func ownedSortKey(value OwnedHook) string {
	return strings.Join([]string{
		value.Adapter,
		value.Event,
		invocationKey(value.Invocation),
		string(value.Proof),
	}, "\x1e")
}

func actionSortKey(value Action) string {
	return strings.Join([]string{
		value.Adapter,
		value.Event,
		string(value.Operation),
		value.SourceFile,
		strings.Join(value.Path, "\x1f"),
		invocationKey(Invocation{
			Form: value.Form, Executable: value.Executable, Args: value.Args,
		}),
	}, "\x1e")
}

func targetKey(adapter, event string) string {
	return adapter + "\x00" + event
}

func locatorKey(value Entry) string {
	return strings.Join([]string{
		value.Adapter,
		value.Event,
		value.SourceFile,
		strings.Join(value.Path, "\x1f"),
	}, "\x1e")
}

func invocationKey(value Invocation) string {
	return strings.Join([]string{
		string(value.Form),
		value.Executable,
		strings.Join(value.Args, "\x1f"),
	}, "\x1e")
}

func containsControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func absoluteCommand(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') ||
			(value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' &&
		(value[2] == '\\' || value[2] == '/')
}
