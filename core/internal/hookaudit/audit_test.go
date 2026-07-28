package hookaudit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func invocation(form Form, executable string, args ...string) Invocation {
	return Invocation{
		Form:       form,
		Executable: executable,
		Args:       args,
	}
}

func desired(
	adapter,
	event,
	source string,
	path []string,
	value Invocation,
) DesiredHook {
	return DesiredHook{
		Adapter:    adapter,
		Event:      event,
		SourceFile: source,
		Path:       path,
		Invocation: value,
	}
}

func entry(
	adapter,
	event,
	source string,
	path []string,
	value Invocation,
) Entry {
	return Entry{
		Adapter:    adapter,
		Event:      event,
		SourceFile: source,
		Path:       path,
		Invocation: value,
	}
}

func TestAuditCodexDistinguishesOwnedDuplicateLegacyAndExternal(t *testing.T) {
	stable := invocation(
		FormShell,
		"/Users/test/Library/Application Support/AgentBell/bin/agentbell-bridge",
		"hook-v1", "--adapter", "codex", "--surface", "desktop",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	legacy := invocation(
		FormShell,
		"/Users/test/Library/Application Support/AgentBell/core/v1/agentbell",
		"emit", "--adapter", "codex", "--surface", "desktop",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	request := Request{
		Desired: []DesiredHook{desired(
			"codex", "Stop", "/Users/test/.codex/hooks.json",
			[]string{"hooks", "Stop", "0", "hooks", "0"},
			stable,
		)},
		OwnedLegacy: []OwnedHook{{
			Adapter: "codex", Event: "Stop", Invocation: legacy,
			Proof: ProofReceipt,
		}},
		Entries: []Entry{
			entry("codex", "Stop", "/Users/test/.codex/hooks.json",
				[]string{"hooks", "Stop", "0", "hooks", "3"},
				invocation(FormShell, "/usr/local/bin/company-hook", "--notify")),
			entry("codex", "Stop", "/Users/test/.codex/hooks.json",
				[]string{"hooks", "Stop", "0", "hooks", "2"}, legacy),
			entry("codex", "Stop", "/Users/test/.codex/hooks.json",
				[]string{"hooks", "Stop", "0", "hooks", "1"}, stable),
			entry("codex", "Stop", "/Users/test/.codex/hooks.json",
				[]string{"hooks", "Stop", "0", "hooks", "0"}, stable),
		},
	}

	report, err := Audit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingKinds(t, report.Findings, []Kind{
		KindCurrentStableBridge,
		KindOwnedDuplicate,
		KindOwnedLegacy,
		KindExternalSameEvent,
	})
	if len(report.Plan.Actions) != 2 {
		t.Fatalf("unexpected reconcile actions: %#v", report.Plan.Actions)
	}
	for _, action := range report.Plan.Actions {
		if action.Operation != OperationRemove ||
			(action.Reason != KindOwnedDuplicate && action.Reason != KindOwnedLegacy) {
			t.Fatalf("unsafe Codex action: %#v", action)
		}
		if action.Executable == "/usr/local/bin/company-hook" {
			t.Fatalf("external hook was planned for removal: %#v", action)
		}
	}
}

func TestAuditClaudePlansStableAddAndExactLegacyRemoval(t *testing.T) {
	stable := invocation(
		FormExec,
		"/opt/agentbell/bin/agentbell-bridge",
		"hook-v1", "--adapter", "claude-code", "--surface", "desktop",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	legacy := invocation(
		FormExec,
		"/opt/agentbell/core/v1/agentbell",
		"emit", "--adapter", "claude-code", "--surface", "desktop",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	request := Request{
		Desired: []DesiredHook{desired(
			"claude-code", "Stop", "/Users/test/.claude/settings.json",
			[]string{"hooks", "Stop", "0", "hooks", "0"},
			stable,
		)},
		OwnedLegacy: []OwnedHook{{
			Adapter: "claude-code", Event: "Stop", Invocation: legacy,
			Proof: ProofManagedReceipt,
		}},
		Entries: []Entry{
			entry("claude-code", "Stop", "/Users/test/.claude/settings.json",
				[]string{"hooks", "Stop", "0", "hooks", "1"},
				invocation(FormExec, "/usr/local/bin/team-hook", "notify")),
			entry("claude-code", "Stop", "/Users/test/.claude/settings.json",
				[]string{"hooks", "Stop", "0", "hooks", "0"}, legacy),
		},
	}

	report, err := Audit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingKinds(t, report.Findings, []Kind{
		KindMissingStableBridge,
		KindOwnedLegacy,
		KindExternalSameEvent,
	})
	if len(report.Plan.Actions) != 2 ||
		report.Plan.Actions[0].Operation != OperationAdd ||
		report.Plan.Actions[1].Operation != OperationRemove {
		t.Fatalf("unexpected Claude reconcile plan: %#v", report.Plan.Actions)
	}
	if report.Plan.Actions[0].Form != FormExec ||
		report.Plan.Actions[0].Executable != stable.Executable ||
		len(report.Plan.Actions[0].Args) == 0 {
		t.Fatalf("add action lost exec-form command: %#v", report.Plan.Actions[0])
	}
}

func TestAuditKimiUnsafeStructureBlocksMutation(t *testing.T) {
	stable := invocation(
		FormShell,
		"/opt/agentbell/bin/agentbell-bridge",
		"hook-v1", "--adapter", "kimi-code", "--surface", "cli",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	legacy := invocation(
		FormShell,
		"/opt/agentbell/core/v1/agentbell",
		"emit", "--adapter", "kimi-code", "--surface", "cli",
		"--runtime", "host", "--stdin", "--fail-open",
	)
	unsafe := entry(
		"kimi-code", "Stop", "/Users/test/.kimi-code/config.toml",
		[]string{"hooks"},
		Invocation{},
	)
	unsafe.UnsafeReason = "top-level inline hooks value cannot be reconciled safely"
	request := Request{
		Desired: []DesiredHook{desired(
			"kimi-code", "Stop", "/Users/test/.kimi-code/config.toml",
			[]string{"hooks", "0"}, stable,
		)},
		OwnedLegacy: []OwnedHook{{
			Adapter: "kimi-code", Event: "Stop", Invocation: legacy,
			Proof: ProofManagedRegion,
		}},
		Entries: []Entry{
			entry("kimi-code", "Stop", "/Users/test/.kimi-code/config.toml",
				[]string{"hooks", "1"}, legacy),
			unsafe,
		},
	}

	report, err := Audit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingKinds(t, report.Findings, []Kind{
		KindMissingStableBridge,
		KindUnsafeStructure,
		KindOwnedLegacy,
	})
	if len(report.Plan.Actions) != 0 || !report.Plan.Blocked {
		t.Fatalf("unsafe Kimi structure produced mutations: %#v", report.Plan)
	}
}

func TestAuditExternalShellAndExecHooksAreAlwaysReportOnly(t *testing.T) {
	request := Request{
		Desired: []DesiredHook{
			desired("generic-shell", "Stop", "/tmp/shell.json", []string{"hooks", "0"},
				invocation(FormShell, "/opt/agentbell/bridge", "hook-v1")),
			desired("generic-exec", "Stop", "/tmp/exec.json", []string{"hooks", "0"},
				invocation(FormExec, "/opt/agentbell/bridge", "hook-v1")),
		},
		Entries: []Entry{
			entry("generic-shell", "Stop", "/tmp/shell.json", []string{"hooks", "1"},
				invocation(FormShell, "/usr/local/bin/external", "--shell-form")),
			entry("generic-exec", "Stop", "/tmp/exec.json", []string{"hooks", "1"},
				invocation(FormExec, "/usr/local/bin/external", "--exec-form")),
		},
	}
	report, err := Audit(request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.ExternalSameEvent != 2 {
		t.Fatalf("external hooks were not reported: %#v", report.Summary)
	}
	for _, action := range report.Plan.Actions {
		if action.Operation == OperationRemove {
			t.Fatalf("external hook removal was planned: %#v", action)
		}
	}
}

func TestAuditJSONIsStableAcrossInputOrderAndContainsNoShellCommand(t *testing.T) {
	stable := invocation(FormExec, `C:\AgentBell\agentbell-bridge.exe`, "hook-v1")
	legacy := invocation(FormExec, `C:\AgentBell\v1\agentbell.exe`, "emit")
	base := Request{
		Desired: []DesiredHook{
			desired("claude-code", "Stop", `C:\Users\test\.claude\settings.json`,
				[]string{"hooks", "Stop", "0"}, stable),
		},
		OwnedLegacy: []OwnedHook{{
			Adapter: "claude-code", Event: "Stop", Invocation: legacy,
			Proof: ProofReceipt,
		}},
		Entries: []Entry{
			entry("claude-code", "Stop", `C:\Users\test\.claude\settings.json`,
				[]string{"hooks", "Stop", "2"}, legacy),
			entry("claude-code", "Stop", `C:\Users\test\.claude\settings.json`,
				[]string{"hooks", "Stop", "1"},
				invocation(FormExec, `C:\Tools\external.exe`, "notify")),
		},
	}
	first, err := AuditJSON(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Entries[0], base.Entries[1] = base.Entries[1], base.Entries[0]
	second, err := AuditJSON(base)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("JSON depends on input order:\n%s\n%s", first, second)
	}
	if !json.Valid(first) || !bytes.HasSuffix(first, []byte("\n")) {
		t.Fatalf("report is not stable JSON: %q", first)
	}
	if bytes.Contains(first, []byte("shellCommand")) ||
		!bytes.Contains(first, []byte(`"args": [`)) ||
		!bytes.Contains(first, []byte(`"path": [`)) {
		t.Fatalf("report contains a shell command or loses arrays:\n%s", first)
	}
}

func TestAuditRejectsAmbiguousProofAndMarksMalformedEntryUnsafe(t *testing.T) {
	stable := invocation(FormExec, "/opt/agentbell/bridge", "hook-v1")
	request := Request{
		Desired: []DesiredHook{
			desired("claude-code", "Stop", "/tmp/settings.json",
				[]string{"hooks", "Stop", "0"}, stable),
		},
		OwnedLegacy: []OwnedHook{{
			Adapter: "claude-code", Event: "Stop",
			Invocation: invocation(FormExec, "/opt/agentbell/old", "emit"),
			Proof:      Proof("guess"),
		}},
	}
	if _, err := Audit(request); err == nil ||
		!strings.Contains(err.Error(), "proof") {
		t.Fatalf("ambiguous ownership proof was accepted: %v", err)
	}

	request.OwnedLegacy = nil
	request.Entries = []Entry{
		entry("claude-code", "Stop", "/tmp/settings.json",
			[]string{"hooks", "Stop", "1"},
			invocation(FormExec, "relative-command", "notify")),
	}
	report, err := Audit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingKinds(t, report.Findings, []Kind{
		KindMissingStableBridge,
		KindUnsafeStructure,
	})
	if !report.Plan.Blocked || len(report.Plan.Actions) != 0 {
		t.Fatalf("malformed entry did not block mutation: %#v", report.Plan)
	}
}

func assertFindingKinds(t *testing.T, findings []Finding, want []Kind) {
	t.Helper()
	if len(findings) != len(want) {
		t.Fatalf("finding count=%d want=%d: %#v", len(findings), len(want), findings)
	}
	for index := range want {
		if findings[index].Kind != want[index] {
			t.Fatalf("finding[%d]=%q want=%q: %#v", index, findings[index].Kind, want[index], findings)
		}
	}
}
