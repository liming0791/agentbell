package adapter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestKimiAdapter(t *testing.T) *KimiAdapter {
	t.Helper()
	root := t.TempDir()
	return &KimiAdapter{
		Executable: filepath.Join(root, "Program Files", "AgentBell", "agentbell.exe"),
		StateDir:   filepath.Join(root, "state"),
		KimiHome:   filepath.Join(root, ".kimi-code"),
		Now: func() time.Time {
			return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return filepath.Join(root, "kimi.exe"), nil
		},
	}
}

func readKimiConfig(t *testing.T, adapter *KimiAdapter) string {
	t.Helper()
	value, err := os.ReadFile(adapter.configPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestKimiFreshInstallVerifyUninstall(t *testing.T) {
	adapter := newTestKimiAdapter(t)

	installed, err := adapter.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || installed.Backup != "" {
		t.Fatalf("fresh install should change without backup: %#v", installed)
	}
	if !strings.Contains(installed.Message, "new Kimi session") {
		t.Fatalf("install omitted the new-session reminder: %#v", installed)
	}
	content := readKimiConfig(t, adapter)
	for _, eventName := range []string{"Stop", "StopFailure", "PermissionRequest"} {
		if !strings.Contains(content, `event = "`+eventName+`"`) {
			t.Fatalf("missing %s hook: %s", eventName, content)
		}
	}
	if !strings.Contains(content, "--adapter kimi-code") ||
		!strings.Contains(content, "agentbell:kimi-code:begin sha256:") {
		t.Fatalf("region is incomplete: %s", content)
	}
	if _, err := os.Stat(adapter.receiptPath()); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
	verified, err := adapter.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify: %#v err=%v", verified, err)
	}

	removed, err := adapter.Uninstall(false)
	if err != nil || !removed.Changed || removed.Backup == "" {
		t.Fatalf("uninstall: %#v err=%v", removed, err)
	}
	if content := readKimiConfig(t, adapter); content != "" {
		t.Fatalf("uninstall must remove the whole region: %q", content)
	}
	if _, err := os.Stat(adapter.receiptPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt must be deleted: %v", err)
	}
}

func TestKimiDiagnoseRequiresRuntimeProofAfterHookChange(t *testing.T) {
	adapterValue := newTestKimiAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	before := adapterValue.Diagnose()
	if !before.Installed || before.RuntimeVerified ||
		!strings.Contains(before.Message, "new Kimi session") {
		t.Fatalf("unexpected pre-runtime diagnosis: %#v", before)
	}
	info, err := os.Stat(adapterValue.configPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAt := info.ModTime().Add(time.Second)
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		kimiAdapterID,
		"task.completed",
		seenAt,
	); err != nil {
		t.Fatal(err)
	}
	after := adapterValue.Diagnose()
	if !after.RuntimeVerified || after.LastSeen == "" {
		t.Fatalf("runtime proof was not reflected: %#v", after)
	}
}

func TestKimiInstallPreservesUserConfigByteForByte(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "# 用户自己的配置，格式与注释必须原样保留\n" +
		"theme = \"dark\"\n" +
		"\n" +
		"[model]\n" +
		"name = \"k1\" # 行内注释\n" +
		"\n" +
		"[[hooks]]\n" +
		"event = \"PreToolUse\"\n" +
		"command = \"user-script --flag\"\n" +
		"timeout = 10\n"
	if err := os.WriteFile(adapter.configPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	installed, err := adapter.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || installed.Backup == "" {
		t.Fatalf("install did not change or backup: %#v", installed)
	}
	afterInstall := readKimiConfig(t, adapter)
	if !strings.HasPrefix(afterInstall, existing) {
		t.Fatalf("user content was not preserved verbatim:\n%s", afterInstall)
	}

	removed, err := adapter.Uninstall(false)
	if err != nil || !removed.Changed {
		t.Fatalf("uninstall: %#v err=%v", removed, err)
	}
	if restored := readKimiConfig(t, adapter); restored != existing {
		t.Fatalf("uninstall must restore the user's original bytes:\n%q\nwant:\n%q", restored, existing)
	}
}

func TestKimiInlineHooksConflictIsRejected(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "hooks = [{event = \"Stop\", command = \"legacy\"}]\n"
	if err := os.WriteFile(adapter.configPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Install(false); err == nil ||
		!strings.Contains(err.Error(), "hooks = ...") {
		t.Fatalf("expected inline-hooks conflict, got %v", err)
	}
	if content := readKimiConfig(t, adapter); content != existing {
		t.Fatalf("conflicting file must not be modified: %q", content)
	}
	backups := filepath.Join(adapter.StateDir, "adapters", kimiAdapterID, "backups")
	if _, err := os.Stat(backups); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no backup may be written on conflict: %v", err)
	}
}

func TestKimiInlineHooksUnderTableIsNotAConflict(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "[plugin]\nhooks = [\"a\", \"b\"]\n"
	if err := os.WriteFile(adapter.configPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Install(false); err != nil {
		t.Fatalf("table-scoped hooks must not conflict: %v", err)
	}
	if _, err := adapter.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestKimiIdempotentReinstall(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	first := readKimiConfig(t, adapter)
	second, err := adapter.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.Backup != "" || second.Message == "" {
		t.Fatalf("reinstall must be a no-op: %#v", second)
	}
	if again := readKimiConfig(t, adapter); again != first {
		t.Fatal("reinstall modified the config")
	}
	if count := strings.Count(first, kimiRegionEndMarker); count != 1 {
		t.Fatalf("region must appear exactly once, got %d", count)
	}
}

func TestKimiUpgradeReplacesRegionWhenCorePathChanges(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	oldContent := readKimiConfig(t, adapter)

	moved := newTestKimiAdapter(t)
	moved.KimiHome = adapter.KimiHome
	moved.StateDir = adapter.StateDir
	moved.Executable = filepath.Join(t.TempDir(), "new-core", "agentbell")
	upgraded, err := moved.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !upgraded.Changed {
		t.Fatal("path change must rewrite the region")
	}
	newContent := readKimiConfig(t, moved)
	if strings.Count(newContent, kimiRegionEndMarker) != 1 {
		t.Fatalf("upgrade must keep exactly one region: %s", newContent)
	}
	oldRegion, _, _ := findKimiRegion(oldContent)
	newRegion, _, _ := findKimiRegion(newContent)
	if oldRegion.hash == newRegion.hash {
		t.Fatal("old command hash survived the upgrade")
	}
	if _, err := moved.Verify(); err != nil {
		t.Fatalf("verify after upgrade: %v", err)
	}
}

func TestKimiVerifyUsesReceiptWhenExecutableMoved(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	// Core 可执行文件移动后，当前命令哈希与区域不一致；
	// Verify 回退到 receipt 中安装时的命令，仍确认区域完好。
	adapter.Executable = filepath.Join(t.TempDir(), "moved", "agentbell")
	verified, err := adapter.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify from receipt: %#v err=%v", verified, err)
	}
}

func TestKimiDryRunWritesNothing(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	result, err := adapter.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run install: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapter.configPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote the config: %v", err)
	}

	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	before := readKimiConfig(t, adapter)
	removed, err := adapter.Uninstall(true)
	if err != nil || !removed.Changed ||
		!strings.Contains(removed.Message, "would be uninstalled") {
		t.Fatalf("dry-run uninstall: %#v err=%v", removed, err)
	}
	if after := readKimiConfig(t, adapter); after != before {
		t.Fatal("dry-run uninstall modified the config")
	}
}

func TestKimiUninstallWithoutRegion(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	missing, err := adapter.Uninstall(false)
	if err != nil || missing.Changed || missing.Message == "" {
		t.Fatalf("missing file: %#v err=%v", missing, err)
	}
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter.configPath(), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := adapter.Uninstall(false)
	if err != nil || plain.Changed || plain.Message == "" {
		t.Fatalf("no region: %#v err=%v", plain, err)
	}
}

func TestKimiVerifyFailureModes(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if _, err := adapter.Verify(); err == nil {
		t.Fatal("verify must fail without a config file")
	}
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(adapter.configPath(), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("theme = \"dark\"\n")
	if _, err := adapter.Verify(); err == nil {
		t.Fatal("verify must fail without a region")
	}

	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	installed := readKimiConfig(t, adapter)

	write(strings.Replace(installed, "event = \"StopFailure\"\n", "", 1))
	if _, err := adapter.Verify(); err == nil ||
		!strings.Contains(err.Error(), "StopFailure") {
		t.Fatalf("verify must fail on a missing event: %v", err)
	}

	write(strings.Replace(installed, "sha256:", "sha256:0", 1))
	if _, err := adapter.Verify(); err == nil {
		t.Fatal("verify must fail on a hash mismatch")
	}

	write(installed + "\n" + installed)
	if _, err := adapter.Verify(); err == nil {
		t.Fatal("verify must fail on duplicate regions")
	}
}

func TestKimiMalformedMarkersAreRejected(t *testing.T) {
	adapter := newTestKimiAdapter(t)
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := kimiRegionBeginPrefix + "abc\n[[hooks]]\nevent = \"Stop\"\n"
	if err := os.WriteFile(adapter.configPath(), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Install(false); err == nil ||
		!strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed-marker error, got %v", err)
	}
	if content := readKimiConfig(t, adapter); content != broken {
		t.Fatal("malformed file must not be modified")
	}
	if _, err := adapter.Uninstall(false); err == nil {
		t.Fatal("uninstall must fail on malformed markers")
	}
}

func TestKimiPlanDiagnoseAndConstructor(t *testing.T) {
	t.Setenv("KIMI_CODE_HOME", filepath.Join(t.TempDir(), ".kimi-code"))
	adapter, err := NewKimiAdapter("relative-agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(adapter.Executable) {
		t.Fatalf("executable is not absolute: %s", adapter.Executable)
	}
	adapter.LookPath = func(string) (string, error) {
		return "", errors.New("missing")
	}
	if adapter.Detect() {
		t.Fatal("detect must be false without binary and config")
	}
	plan := adapter.Plan()
	if plan.Adapter != kimiAdapterID || plan.Detected || len(plan.Changes) != 2 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	diagnose := adapter.Diagnose()
	if diagnose.Installed || diagnose.Message == "" {
		t.Fatalf("unexpected diagnosis: %#v", diagnose)
	}

	// config.toml 存在时即使二进制不在 PATH 也算检测到。
	if err := os.MkdirAll(adapter.KimiHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter.configPath(), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if !adapter.Detect() {
		t.Fatal("detect must be true when config.toml exists")
	}
}

func TestKimiRejectsUnsafeExecutablePath(t *testing.T) {
	for _, bad := range []string{"bad\npath", "bad\rpath", "bad\"path", "bad\x00path"} {
		adapter := newTestKimiAdapter(t)
		adapter.Executable = bad
		if _, err := adapter.Install(true); err == nil {
			t.Fatalf("expected unsafe executable error for %q", bad)
		}
	}
}

func TestKimiTomlBasicStringEscapesWindowsPath(t *testing.T) {
	escaped := tomlBasicString(`'C:\Program Files\AgentBell\agentbell.exe'`)
	want := `"'C:\\Program Files\\AgentBell\\agentbell.exe'"`
	if escaped != want {
		t.Fatalf("escaped %s, want %s", escaped, want)
	}
	if got := tomlBasicString(`say "hi"`); got != `"say \"hi\""` {
		t.Fatalf("quote escaping: %s", got)
	}
}

func TestKimiAdapterConformanceFixtures(t *testing.T) {
	fixtures := []struct {
		name       string
		executable string
	}{
		{name: "windows", executable: `C:\Program Files\AgentBell\agentbell.exe`},
		{name: "macos", executable: "/Applications/AgentBell/agentbell"},
		{name: "linux", executable: "/opt/agentbell/bin/agentbell"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			adapterValue := newTestKimiAdapter(t)
			adapterValue.Executable = fixture.executable

			plan := adapterValue.Plan()
			if plan.Executable != fixture.executable || !plan.Detected {
				t.Fatalf("unexpected plan: %#v", plan)
			}
			firstInstall, err := adapterValue.Install(false)
			if err != nil || !firstInstall.Changed {
				t.Fatalf("first install: %#v err=%v", firstInstall, err)
			}
			secondInstall, err := adapterValue.Install(false)
			if err != nil || secondInstall.Changed {
				t.Fatalf("second install was not idempotent: %#v err=%v", secondInstall, err)
			}
			verified, err := adapterValue.Verify()
			if err != nil || !verified.Installed {
				t.Fatalf("verify: %#v err=%v", verified, err)
			}
			config := readKimiConfig(t, adapterValue)
			escapedExecutable := strings.ReplaceAll(fixture.executable, `\`, `\\`)
			if !strings.Contains(config, escapedExecutable) {
				t.Fatalf("hook does not use the escaped Core path: %s", config)
			}
			firstUninstall, err := adapterValue.Uninstall(false)
			if err != nil || !firstUninstall.Changed {
				t.Fatalf("first uninstall: %#v err=%v", firstUninstall, err)
			}
			secondUninstall, err := adapterValue.Uninstall(false)
			if err != nil || secondUninstall.Changed {
				t.Fatalf(
					"second uninstall was not idempotent: %#v err=%v",
					secondUninstall,
					err,
				)
			}
			diagnosis := adapterValue.Diagnose()
			if diagnosis.Installed || diagnosis.Message == "" {
				t.Fatalf("unexpected diagnosis: %#v", diagnosis)
			}
		})
	}
}
