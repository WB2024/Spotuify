// Command spotuify is a terminal UI for deep-exporting Spotify playlists
// (full metadata, tracklists, and cover art) to JSON and CSV.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"spotuify/internal/config"
	"spotuify/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "spotuify:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	return tui.Run(runCtx, cancel, cfg)
}
