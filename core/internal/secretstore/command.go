package secretstore

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

const maximumCommandOutputBytes = 256

type execRunner struct{}

type commandExitError struct {
	code int
}

func (failure commandExitError) Error() string {
	return "secret store command failed"
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	overflow bool
}

func (writer *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := writer.maximum - writer.buffer.Len()
	if remaining < len(value) {
		writer.overflow = true
		if remaining > 0 {
			_, _ = writer.buffer.Write(value[:remaining])
		}
		return originalLength, nil
	}
	_, _ = writer.buffer.Write(value)
	return originalLength, nil
}

func (execRunner) Run(
	ctx context.Context,
	name string,
	args []string,
	stdin []byte,
) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	if len(stdin) != 0 {
		command.Stdin = bytes.NewReader(stdin)
	}
	stdout := boundedBuffer{maximum: maximumCommandOutputBytes}
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			return nil, commandExitError{code: failure.ExitCode()}
		}
		return nil, ErrBackend
	}
	if stdout.overflow {
		return nil, ErrBackend
	}
	return stdout.buffer.Bytes(), nil
}
