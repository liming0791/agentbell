//go:build !windows

package bridge

import (
	"context"
	"io"
)

func runServiceChild(
	ctx context.Context,
	executable string,
	args []string,
	input []byte,
	stdout io.Writer,
	stderr io.Writer,
) error {
	return (ExecRunner{}).Run(ctx, executable, args, input, stdout, stderr)
}
