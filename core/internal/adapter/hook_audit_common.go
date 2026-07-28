package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

const (
	maximumHookAuditBytes      = 1 << 20
	maximumAuditReceiptBytes   = 64 << 10
	maximumAuditJSONDepth      = 128
	maximumHookAuditJSONTokens = 1 << 18
	maximumReceiptJSONTokens   = 1 << 15
)

func stableAuditInvocation(
	invocation hookInvocation,
	form hookaudit.Form,
) (hookaudit.Invocation, error) {
	if invocation.BridgeProtocol != stableBridgeProtocol ||
		invocation.ActivationGeneration == 0 ||
		invocation.Executable == "" ||
		len(invocation.Args) == 0 ||
		invocation.Args[0] != "hook-v1" {
		return hookaudit.Invocation{}, errors.New(
			"Hook audit requires a configured stable bridge and active generation",
		)
	}
	return hookaudit.Invocation{
		Form:       form,
		Executable: invocation.Executable,
		Args:       append([]string(nil), invocation.Args...),
	}, nil
}

func readHookAuditFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maximumHookAuditBytes+1))
	if err != nil {
		return nil, err
	}
	if len(value) > maximumHookAuditBytes {
		return nil, fmt.Errorf(
			"Hook audit source %s exceeds %d bytes",
			path,
			maximumHookAuditBytes,
		)
	}
	return value, nil
}

func readHookAuditJSONObject(path string) (map[string]any, error) {
	value, err := readHookAuditFile(path)
	if err != nil {
		return nil, err
	}
	if err := validateUniqueJSONKeys(
		value,
		maximumHookAuditJSONTokens,
	); err != nil {
		return nil, fmt.Errorf("parse Hook audit source %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse Hook audit source %s: %w", path, err)
	}
	if root == nil {
		return nil, fmt.Errorf("Hook audit source %s must be an object", path)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("Hook audit source %s has trailing JSON", path)
		}
		return nil, fmt.Errorf("parse Hook audit source %s trailing data: %w", path, err)
	}
	return root, nil
}

func readAuditReceipt(path string, destination any) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maximumAuditReceiptBytes+1))
	if err != nil || len(value) > maximumAuditReceiptBytes {
		return false
	}
	if validateUniqueJSONKeys(value, maximumReceiptJSONTokens) != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	var trailing json.RawMessage
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

// validateUniqueJSONKeys walks the token stream before typed decoding so Go's
// last-key-wins JSON behavior can never change Hook or receipt ownership.
// Depth and token limits make adversarial but byte-bounded documents safe to
// inspect without unbounded recursion or work.
func validateUniqueJSONKeys(value []byte, maximumTokens int) error {
	if len(value) == 0 || maximumTokens <= 0 {
		return errors.New("invalid JSON document")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	tokens := 0
	if err := validateUniqueJSONValue(
		decoder,
		0,
		&tokens,
		maximumTokens,
	); err != nil {
		return err
	}
	if _, err := nextAuditJSONToken(
		decoder,
		&tokens,
		maximumTokens,
	); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return errors.New("invalid trailing JSON data")
	}
	return nil
}

func validateUniqueJSONValue(
	decoder *json.Decoder,
	depth int,
	tokens *int,
	maximumTokens int,
) error {
	if depth > maximumAuditJSONDepth {
		return errors.New("JSON nesting exceeds audit limit")
	}
	token, err := nextAuditJSONToken(decoder, tokens, maximumTokens)
	if err != nil {
		return errors.New("invalid JSON document")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			rawName, err := nextAuditJSONToken(
				decoder,
				tokens,
				maximumTokens,
			)
			if err != nil {
				return errors.New("invalid JSON object")
			}
			name, ok := rawName.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := keys[name]; exists {
				return errors.New("duplicate JSON key")
			}
			keys[name] = struct{}{}
			if err := validateUniqueJSONValue(
				decoder,
				depth+1,
				tokens,
				maximumTokens,
			); err != nil {
				return err
			}
		}
		closing, err := nextAuditJSONToken(
			decoder,
			tokens,
			maximumTokens,
		)
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueJSONValue(
				decoder,
				depth+1,
				tokens,
				maximumTokens,
			); err != nil {
				return err
			}
		}
		closing, err := nextAuditJSONToken(
			decoder,
			tokens,
			maximumTokens,
		)
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func nextAuditJSONToken(
	decoder *json.Decoder,
	tokens *int,
	maximumTokens int,
) (json.Token, error) {
	if *tokens >= maximumTokens {
		return nil, errors.New("JSON token count exceeds audit limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	*tokens = *tokens + 1
	return token, nil
}

func parseAuditShellInvocation(value string) (hookaudit.Invocation, error) {
	tokens, err := tokenizeAuditShell(value)
	if err != nil {
		return hookaudit.Invocation{}, err
	}
	if len(tokens) == 0 {
		return hookaudit.Invocation{}, errors.New("shell-form Hook command is empty")
	}
	return hookaudit.Invocation{
		Form:       hookaudit.FormShell,
		Executable: tokens[0],
		Args:       append([]string(nil), tokens[1:]...),
	}, nil
}

// tokenizeAuditShell accepts quoting and escaping but rejects substitutions,
// redirections, pipelines, separators, and globbing. It never invokes a shell.
func tokenizeAuditShell(value string) ([]string, error) {
	var result []string
	for index := 0; index < len(value); {
		for index < len(value) && unicode.IsSpace(rune(value[index])) {
			index++
		}
		if index == len(value) {
			break
		}
		var token strings.Builder
		started := false
		for index < len(value) {
			character := value[index]
			if unicode.IsSpace(rune(character)) {
				break
			}
			started = true
			switch character {
			case '\'':
				index++
				for index < len(value) && value[index] != '\'' {
					token.WriteByte(value[index])
					index++
				}
				if index >= len(value) {
					return nil, errors.New("unterminated single quote in Hook command")
				}
				index++
			case '"':
				index++
				for index < len(value) && value[index] != '"' {
					if value[index] == '$' || value[index] == '`' {
						return nil, errors.New(
							"substitution is not allowed in Hook command",
						)
					}
					token.WriteByte(value[index])
					index++
				}
				if index >= len(value) {
					return nil, errors.New("unterminated double quote in Hook command")
				}
				index++
			case '\\':
				index++
				if index >= len(value) {
					return nil, errors.New("unterminated escape in Hook command")
				}
				token.WriteByte(value[index])
				index++
			case '$', '`', '|', '&', ';', '<', '>', '(', ')', '*', '?', '[', ']':
				return nil, fmt.Errorf(
					"unsupported shell operator %q in Hook command",
					character,
				)
			default:
				if character == 0 || character == '\r' || character == '\n' {
					return nil, errors.New("control character in Hook command")
				}
				token.WriteByte(character)
				index++
			}
		}
		if !started {
			return nil, errors.New("invalid empty token in Hook command")
		}
		result = append(result, token.String())
	}
	return result, nil
}
