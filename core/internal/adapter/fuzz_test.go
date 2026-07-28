package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

func FuzzHookConflictParsers(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`/opt/agentbell-bridge hook-v1 --adapter codex`),
		[]byte(`"/Applications/Agent Bell/agentbell-bridge" hook-v1`),
		[]byte(`agentbell $(private-command)`),
		[]byte("[[hooks]]\nevent = \"Stop\"\ncommand = \"/opt/agentbell\"\n"),
		[]byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/agentbell"}]}]}}`),
		[]byte(`{"hooks":{},"hooks":{"Stop":[]}}`),
		[]byte(`{"hooks":{"Stop":[{"hooks":[{"command":"one","command":"two"}]}]}}`),
		[]byte(`{"hooks":[]}`),
		[]byte{0xff, 0x00, '"', '\''},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maximumHookAuditBytes {
			return
		}
		firstJSONErr := validateUniqueJSONKeys(
			raw,
			maximumHookAuditJSONTokens,
		)
		secondJSONErr := validateUniqueJSONKeys(
			raw,
			maximumHookAuditJSONTokens,
		)
		if (firstJSONErr == nil) != (secondJSONErr == nil) ||
			(firstJSONErr != nil &&
				firstJSONErr.Error() != secondJSONErr.Error()) {
			t.Fatal("Hook JSON duplicate-key validation was not deterministic")
		}
		text := string(raw)
		if first, err := parseAuditShellInvocation(text); err == nil {
			second, secondErr := parseAuditShellInvocation(text)
			if secondErr != nil || !reflect.DeepEqual(first, second) {
				t.Fatal("Hook shell parser was not deterministic")
			}
		}
		if first, err := parseKimiAuditBlocks(text); err == nil {
			second, secondErr := parseKimiAuditBlocks(text)
			if secondErr != nil || !reflect.DeepEqual(first, second) {
				t.Fatal("Kimi Hook parser was not deterministic")
			}
			previousStart := -1
			for index, block := range first {
				if block.index != index ||
					block.start < 0 ||
					block.start > len(text) ||
					block.start < previousStart {
					t.Fatal("Kimi Hook parser returned an invalid block boundary")
				}
				previousStart = block.start
			}
		}

		root, ok := decodeFuzzHookObject(raw)
		if !ok {
			return
		}
		desired := hookaudit.Invocation{
			Form:       hookaudit.FormShell,
			Executable: "/opt/agentbell-bridge",
			Args:       []string{"hook-v1", "--adapter", "codex"},
		}
		request, err := normalizeJSONAuditRequest(
			root,
			"codex",
			"/tmp/hooks.json",
			[]string{"Stop"},
			desired,
			func(handler map[string]any) (hookaudit.Invocation, string) {
				command, ok := handler["command"].(string)
				if !ok {
					return hookaudit.Invocation{}, "command must be a string"
				}
				value, err := parseAuditShellInvocation(command)
				if err != nil {
					return hookaudit.Invocation{}, "command is unsafe"
				}
				return value, ""
			},
		)
		if err != nil {
			return
		}
		first, err := hookaudit.AuditJSON(request)
		if err != nil {
			return
		}
		second, err := hookaudit.AuditJSON(request)
		if err != nil || !bytes.Equal(first, second) {
			t.Fatal("Hook conflict report was not deterministic")
		}
	})
}

func decodeFuzzHookObject(raw []byte) (map[string]any, bool) {
	if validateUniqueJSONKeys(raw, maximumHookAuditJSONTokens) != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return root, true
}
