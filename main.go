package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"webcam-stream/stream/config"
	"webcam-stream/stream/server"
	"webcam-stream/stream/transcoder"
	"webcam-stream/web"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv("DEBUG") == "1" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log); err != nil {
		log.Error("gagal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	sup, err := transcoder.New(cfg, log.With("component", "ffmpeg"))
	if err != nil {
		return err
	}

	srv, err := server.New(server.Options{
		Listen:    cfg.Listen,
		HLSDir:    cfg.HLSDir,
		AuthToken: cfg.AuthToken,
		Static:    web.Static(),
		Status:    sup.Status,
		Log:       log.With("component", "http"),
	})
	if err != nil {
		return err
	}

	if cfg.AuthTokenGenerated {
		fmt.Fprintf(os.Stderr, "\n  AUTH_TOKEN not set, using temporary token: %s\n\n", cfg.AuthToken)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sup.Run(ctx)
	}()

	err = srv.Run(ctx)
	stop()
	wg.Wait()
	log.Info("selesai")
	return err
}
