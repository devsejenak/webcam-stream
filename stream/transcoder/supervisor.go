package transcoder

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"webcam-stream/stream/config"
)

type Supervisor struct {
	cfg    *config.Config
	ffmpeg string
	log    *slog.Logger

	mu        sync.RWMutex
	running   bool
	lastError string
	restarts  int
}

func New(cfg *config.Config, log *slog.Logger) (*Supervisor, error) {
	path, err := resolveFFmpeg(cfg.FFmpegPath)
	if err != nil {
		return nil, err
	}
	return &Supervisor{cfg: cfg, ffmpeg: path, log: log}, nil
}

func (s *Supervisor) FFmpegPath() string { return s.ffmpeg }

func resolveFFmpeg(explicit string) (string, error) {
	name := "ffmpeg"
	if explicit != "" {
		name = explicit
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found (%q), install it or set FFMPEG_PATH: %w", name, err)
	}
	return p, nil
}

type Status struct {
	Running   bool   `json:"running"`
	Ready     bool   `json:"ready"`
	Restarts  int    `json:"restarts"`
	LastError string `json:"last_error,omitempty"`
}

func (s *Supervisor) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Running: s.running, Restarts: s.restarts, LastError: s.lastError}
	if fi, err := os.Stat(filepath.Join(s.cfg.HLSDir, PlaylistName)); err == nil {
		maxAge := time.Duration(s.cfg.HLSTime*3) * time.Second
		st.Ready = s.running && time.Since(fi.ModTime()) < maxAge
	}
	return st
}

func (s *Supervisor) Run(ctx context.Context) {
	backoff := time.Second
	for {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		s.log.Warn("ffmpeg stopped, restarting", "err", err, "in", backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	if err := os.RemoveAll(s.cfg.HLSDir); err != nil {
		return fmt.Errorf("remove HLS dir: %w", err)
	}
	if err := os.MkdirAll(s.cfg.HLSDir, 0o755); err != nil {
		return fmt.Errorf("create HLS dir: %w", err)
	}

	args := BuildArgs(s.cfg)
	cmd := exec.Command(s.ffmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	s.log.Info("starting ffmpeg", "input", s.cfg.Input, "camera", s.cfg.Camera, "size", s.cfg.VideoSize, "fps", s.cfg.Framerate, "encoder", s.cfg.Encoder)
	s.log.Debug("ffmpeg args", "cmd", s.ffmpeg+" "+strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}
	s.setRunning(true, "")

	go s.pipeLogs(stderr)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		s.setRunning(false, errString(err))
		if err == nil {
			err = errors.New("ffmpeg exited unexpectedly")
		}
		return err
	case <-ctx.Done():
		s.stop(cmd, stdin, done)
		s.setRunning(false, "")
		return ctx.Err()
	}
}

func (s *Supervisor) stop(cmd *exec.Cmd, stdin io.WriteCloser, done <-chan error) {
	s.log.Info("stopping ffmpeg")
	_, _ = io.WriteString(stdin, "q")
	_ = stdin.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.log.Warn("ffmpeg did not stop in 5s, killing")
		_ = cmd.Process.Kill()
		<-done
	}
}

func (s *Supervisor) pipeLogs(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.log.Warn("ffmpeg", "msg", line)
		s.mu.Lock()
		s.lastError = line
		s.mu.Unlock()
	}
}

func (s *Supervisor) setRunning(r bool, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = r
	if lastErr != "" {
		s.lastError = lastErr
	} else if r {
		s.lastError = ""
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
