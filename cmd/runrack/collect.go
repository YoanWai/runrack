package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/YoanWai/runrack/internal/demo"
	"github.com/YoanWai/runrack/internal/state"
)

func collectOnce(opts options) (state.Snapshot, error) {
	if opts.demo {
		now := time.Now()
		return demo.World(now, now), nil
	}
	if _, err := os.Stat("/proc/stat"); err != nil {
		return state.Snapshot{}, errors.New("no /proc: runrack discovers runners on Linux only (try --demo)")
	}
	engine, _ := newEngine(opts)
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return engine.Once(ctx), nil
}
