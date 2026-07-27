package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/liming0791/agentbell/core/internal/bridge"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(bridge.New().Run(
		ctx,
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
	))
}
