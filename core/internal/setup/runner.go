package setup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner executes external commands. Capture collects stdout and is used for
// read-only queries; Interactive wires the process stdio through so the user
// can interact with the child command directly (credentials never pass
// through AgentBell).
type Runner interface {
	Capture(ctx context.Context, name string, args ...string) ([]byte, error)
	Interactive(ctx context.Context, name string, args ...string) error
}

// ExecRunner implements Runner with os/exec.
type ExecRunner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (runner ExecRunner) Capture(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stderr = &limitedBuffer{buffer: &stderr, remaining: 4096}
	output, err := command.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return output, err
	}
	return output, nil
}

func (runner ExecRunner) Interactive(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = defaultReader(runner.Stdin)
	command.Stdout = defaultWriter(runner.Stdout)
	command.Stderr = defaultWriter(runner.Stderr)
	return command.Run()
}

func defaultReader(reader io.Reader) io.Reader {
	if reader != nil {
		return reader
	}
	return os.Stdin
}

func defaultWriter(writer io.Writer) io.Writer {
	if writer != nil {
		return writer
	}
	return os.Stdout
}

type limitedBuffer struct {
	buffer    *bytes.Buffer
	remaining int
}

func (writer *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if writer.remaining <= 0 {
		return original, nil
	}
	if len(value) > writer.remaining {
		value = value[:writer.remaining]
	}
	_, _ = writer.buffer.Write(value)
	writer.remaining -= len(value)
	return original, nil
}

// Prompter asks the user questions during the interactive setup.
type Prompter interface {
	Confirm(question string) (bool, error)
	Input(question string) (string, error)
	Select(question string, options []string) (int, error)
}

// StdioPrompter implements Prompter over a reader/writer pair.
type StdioPrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func NewStdioPrompter(in io.Reader, out io.Writer) *StdioPrompter {
	return &StdioPrompter{reader: bufio.NewReader(in), out: out}
}

func (prompter *StdioPrompter) Confirm(question string) (bool, error) {
	fmt.Fprintf(prompter.out, "%s [Y/n] ", question)
	answer, err := prompter.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("无法识别的回答 %q", answer)
	}
}

func (prompter *StdioPrompter) Input(question string) (string, error) {
	fmt.Fprintf(prompter.out, "%s: ", question)
	return prompter.readLine()
}

func (prompter *StdioPrompter) Select(question string, options []string) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("没有可选项：%s", question)
	}
	fmt.Fprintln(prompter.out, question)
	for index, option := range options {
		fmt.Fprintf(prompter.out, "  %d) %s\n", index+1, option)
	}
	fmt.Fprint(prompter.out, "请输入序号: ")
	answer, err := prompter.readLine()
	if err != nil {
		return -1, err
	}
	var choice int
	if _, err := fmt.Sscanf(answer, "%d", &choice); err != nil ||
		choice < 1 || choice > len(options) {
		return -1, fmt.Errorf("无效的选择 %q", answer)
	}
	return choice - 1, nil
}

func (prompter *StdioPrompter) readLine() (string, error) {
	line, err := prompter.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
