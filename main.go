package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"webcam-to-cctv/stream/alert"
	"webcam-to-cctv/stream/config"
	"webcam-to-cctv/stream/detect"
	"webcam-to-cctv/stream/notify"
	"webcam-to-cctv/stream/server"
	"webcam-to-cctv/stream/transcoder"
	"webcam-to-cctv/web"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv("DEBUG") == "1" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
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

	var alerter *alert.Alerter
	if cfg.Alert.Enabled() {
		if !cfg.Detect.Enabled {
			log.Warn("Telegram alerts configured but DETECT_ENABLED is false; alerts disabled")
		} else {
			a := cfg.Alert
			alerter = alert.New(
				notify.NewTelegram(a.TelegramToken, a.TelegramChatID, a.TelegramThreadID),
				alert.Options{
					FFmpeg:   sup.FFmpegPath(),
					Cooldown: a.Cooldown,
					Photos:   a.Photos,
					Log:      log.With("component", "alert"),
				})
			log.Info("telegram alerts enabled", "cooldown", a.Cooldown, "photos", a.Photos)
		}
	}

	var watcher *detect.Watcher
	if cfg.Detect.Enabled {
		w, cleanup, err := setupDetection(cfg, sup.FFmpegPath(), alerter, log.With("component", "detect"))
		if err != nil {
			log.Error("detection disabled", "err", err)
		} else {
			defer cleanup()
			watcher = w
		}
	}
	if watcher == nil {
		alerter = nil
	}

	opts := server.Options{
		Listen:    cfg.Listen,
		HLSDir:    cfg.HLSDir,
		AuthToken: cfg.AuthToken,
		Static:    web.Static(),
		Status:    sup.Status,
		Log:       log.With("component", "http"),
	}
	if watcher != nil {
		opts.Detections = func() any { return watcher.Snapshot() }
	}
	srv, err := server.New(opts)
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
	if watcher != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			watcher.Run(ctx)
		}()
	}
	if alerter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			alerter.Run(ctx)
		}()
	}

	err = srv.Run(ctx)
	stop() // if the server failed to start, stop ffmpeg and detection too
	wg.Wait()
	log.Info("stopped")
	return err
}

func setupDetection(cfg *config.Config, ffmpeg string, alerter *alert.Alerter, log *slog.Logger) (*detect.Watcher, func(), error) {
	d := cfg.Detect
	if err := detect.Init(d.Library); err != nil {
		return nil, nil, err
	}
	model, err := detect.NewYOLOX(d.Model, detect.Options{Threads: d.Threads})
	if err != nil {
		detect.Shutdown()
		return nil, nil, err
	}

	opts := detect.WatcherOptions{
		HLSDir:     cfg.HLSDir,
		Playlist:   transcoder.PlaylistName,
		FFmpeg:     ffmpeg,
		FPS:        d.FPS,
		MinScore:   float32(d.MinScore),
		AllClasses: d.AllClasses,
		Log:        log,
	}
	if alerter != nil {
		opts.OnResult = alerter.OnResult
	}
	w := detect.NewWatcher(model, opts)
	inW, inH := model.InputSize()
	log.Info("detection enabled", "model", d.Model, "input", fmt.Sprintf("%dx%d", inW, inH), "fps", d.FPS, "min_score", d.MinScore)

	cleanup := func() {
		model.Close()
		detect.Shutdown()
	}
	return w, cleanup, nil
}
