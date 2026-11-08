package alert

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"webcam-to-cctv/stream/detect"
)

type Sender interface {
	SendPhotos(ctx context.Context, photos [][]byte, caption string) error
}

type Options struct {
	FFmpeg   string
	Cooldown time.Duration
	Photos   int
	Retry    time.Duration
	Log      *slog.Logger
}

type Alerter struct {
	sender Sender
	opts   Options
	log    *slog.Logger
	queue  chan event

	mu   sync.Mutex
	next time.Time
}

type event struct {
	at      time.Time
	segment []byte
	frames  []detect.FrameResult
}

func New(sender Sender, opts Options) *Alerter {
	if opts.Photos <= 0 {
		opts.Photos = 1
	}
	if opts.Photos > 10 {
		opts.Photos = 10
	}
	if opts.Retry <= 0 {
		opts.Retry = time.Minute
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Alerter{sender: sender, opts: opts, log: opts.Log, queue: make(chan event, 1)}
}

func (a *Alerter) OnResult(segmentPath string, frames []detect.FrameResult) {
	hits := personFrames(frames)
	if len(hits) == 0 {
		return
	}

	now := time.Now()
	a.mu.Lock()
	if now.Before(a.next) {
		a.mu.Unlock()
		return
	}
	a.next = now.Add(a.opts.Cooldown)
	a.mu.Unlock()

	data, err := os.ReadFile(segmentPath)
	if err != nil {
		a.log.Warn("alert: read segment", "err", err)
		a.retryAfter(now)
		return
	}
	select {
	case a.queue <- event{at: now, segment: data, frames: hits}:
	default:
	}
}

func (a *Alerter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-a.queue:
			if err := a.send(ctx, ev); err != nil {
				if ctx.Err() != nil {
					return
				}
				a.log.Warn("alert failed", "err", err, "retry_in", a.opts.Retry)
				a.retryAfter(time.Now())
				continue
			}
			a.log.Info("alert sent", "persons", maxPersons(ev.frames), "next_after", a.opts.Cooldown)
		}
	}
}

func (a *Alerter) retryAfter(from time.Time) {
	a.mu.Lock()
	if t := from.Add(a.opts.Retry); t.Before(a.next) {
		a.next = t
	}
	a.mu.Unlock()
}

func (a *Alerter) send(ctx context.Context, ev event) error {
	var photos [][]byte
	for _, f := range pick(ev.frames, a.opts.Photos) {
		jpg, err := a.snapshot(ctx, ev.segment, f)
		if err != nil {
			a.log.Debug("alert: snapshot failed", "offset", f.Offset, "err", err)
			continue
		}
		photos = append(photos, jpg)
	}
	if len(photos) == 0 {
		return fmt.Errorf("no snapshot could be extracted")
	}

	best := float32(0)
	for _, f := range ev.frames {
		for _, o := range f.Objects {
			if o.Label == "person" && o.Score > best {
				best = o.Score
			}
		}
	}
	caption := fmt.Sprintf("🚨 Person detected (%d)\n%s\nconfidence %.0f%%",
		maxPersons(ev.frames), ev.at.Format("2006-01-02 15:04:05"), best*100)

	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return a.sender.SendPhotos(sctx, photos, caption)
}

func (a *Alerter) snapshot(ctx context.Context, segment []byte, f detect.FrameResult) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.opts.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "pipe:0",
		"-ss", fmt.Sprintf("%.3f", f.Offset),
		"-frames:v", "1",
		"-c:v", "mjpeg", "-q:v", "3", "-f", "image2pipe", "pipe:1",
	)
	cmd.Stdin = bytes.NewReader(segment)
	var stdout bytes.Buffer
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg returned no frame")
	}

	img, err := jpeg.Decode(&stdout)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	fw, fh := float32(b.Dx()), float32(b.Dy())
	var dets []detect.Detection
	for _, o := range f.Objects {
		if o.Label != "person" {
			continue
		}
		dets = append(dets, detect.Detection{
			Class: 0,
			Label: o.Label,
			Score: o.Score,
			Box: image.Rect(int(o.X*fw), int(o.Y*fh), int((o.X+o.W)*fw), int((o.Y+o.H)*fh)).
				Add(b.Min),
		})
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, detect.Annotate(img, dets), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func personFrames(frames []detect.FrameResult) []detect.FrameResult {
	var out []detect.FrameResult
	for _, f := range frames {
		if countPersons(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

func countPersons(f detect.FrameResult) int {
	n := 0
	for _, o := range f.Objects {
		if o.Label == "person" {
			n++
		}
	}
	return n
}

func maxPersons(frames []detect.FrameResult) int {
	m := 0
	for _, f := range frames {
		m = max(m, countPersons(f))
	}
	return m
}

func pick(frames []detect.FrameResult, n int) []detect.FrameResult {
	if len(frames) <= n {
		return frames
	}
	out := make([]detect.FrameResult, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, frames[i*(len(frames)-1)/max(n-1, 1)])
	}
	return out
}
