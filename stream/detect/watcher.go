package detect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WatcherOptions configures a Watcher.
type WatcherOptions struct {
	HLSDir     string  // folder with the playlist and segments
	Playlist   string  // playlist file name, e.g. "stream.m3u8"
	FFmpeg     string  // path to ffmpeg, used to extract frames from segments
	FPS        float64 // frames analysed per second of video
	MinScore   float32 // minimum detection score
	AllClasses bool    // report all classes, not only person
	Keep       int     // number of segments to keep results for (default 30)
	Log        *slog.Logger

	OnResult func(segmentPath string, frames []FrameResult)
}

// Object is one detection, with the box normalized to 0..1 of the frame.
type Object struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	W     float32 `json:"w"`
	H     float32 `json:"h"`
}

// FrameResult holds detections for one frame, Offset seconds into its segment.
type FrameResult struct {
	Offset  float64  `json:"t"`
	Objects []Object `json:"objects"`
}

// Snapshot is what the viewer polls: recent results keyed by segment file name.
type Snapshot struct {
	Enabled     bool                     `json:"enabled"`
	FPS         float64                  `json:"fps"`
	InferenceMS float64                  `json:"inference_ms"`
	Processed   int                      `json:"processed"`
	Skipped     int                      `json:"skipped"`
	LastError   string                   `json:"last_error,omitempty"`
	Segments    map[string][]FrameResult `json:"segments"`
}

// Watcher runs YOLOX on every new HLS segment written by the transcoder.
// It only reads files, so the live stream keeps running even if detection
// is slow or fails.
type Watcher struct {
	model *YOLOX
	opts  WatcherOptions
	log   *slog.Logger

	seen map[string]time.Time

	mu        sync.RWMutex
	results   map[string][]FrameResult
	order     []string
	inferAvg  float64
	processed int
	skipped   int
	lastError string
}

func NewWatcher(model *YOLOX, opts WatcherOptions) *Watcher {
	if opts.Keep <= 0 {
		opts.Keep = 30
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Watcher{
		model:   model,
		opts:    opts,
		log:     opts.Log,
		seen:    map[string]time.Time{},
		results: map[string][]FrameResult{},
	}
}

// Run polls the playlist until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		w.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Snapshot returns a copy of the current results.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	segs := make(map[string][]FrameResult, len(w.results))
	for k, v := range w.results {
		segs[k] = v
	}
	return Snapshot{
		Enabled:     true,
		FPS:         w.opts.FPS,
		InferenceMS: w.inferAvg,
		Processed:   w.processed,
		Skipped:     w.skipped,
		LastError:   w.lastError,
		Segments:    segs,
	}
}

func (w *Watcher) poll(ctx context.Context) {
	names, err := readPlaylist(filepath.Join(w.opts.HLSDir, w.opts.Playlist))
	if err != nil {
		return // playlist not written yet, or transcoder restarting
	}

	type pending struct {
		name string
		mod  time.Time
	}
	var todo []pending
	inPlaylist := make(map[string]bool, len(names))
	for _, n := range names {
		inPlaylist[n] = true
		fi, err := os.Stat(filepath.Join(w.opts.HLSDir, n))
		if err != nil {
			continue
		}
		if mod, ok := w.seen[n]; ok && mod.Equal(fi.ModTime()) {
			continue
		}
		todo = append(todo, pending{n, fi.ModTime()})
	}
	for n := range w.seen {
		if !inPlaylist[n] {
			delete(w.seen, n)
		}
	}

	if len(todo) > 0 {
		w.mu.Lock()
		for _, p := range todo {
			delete(w.results, p.name)
		}
		w.mu.Unlock()
	}

	// If detection fell behind, skip old segments and catch up with live.
	const maxBacklog = 2
	if len(todo) > maxBacklog {
		skip := todo[:len(todo)-maxBacklog]
		for _, p := range skip {
			w.seen[p.name] = p.mod
		}
		w.mu.Lock()
		w.skipped += len(skip)
		w.mu.Unlock()
		w.log.Debug("detection behind, skipping segments", "count", len(skip))
		todo = todo[len(todo)-maxBacklog:]
	}

	for _, p := range todo {
		if ctx.Err() != nil {
			return
		}
		w.seen[p.name] = p.mod
		frames, err := w.analyse(ctx, p.name)
		w.mu.Lock()
		if err != nil {
			w.lastError = err.Error()
			w.mu.Unlock()
			w.log.Warn("detect segment failed", "segment", p.name, "err", err)
			continue
		}
		w.store(p.name, frames)
		w.mu.Unlock()
		if w.opts.OnResult != nil {
			w.opts.OnResult(filepath.Join(w.opts.HLSDir, p.name), frames)
		}
	}
}

func (w *Watcher) store(name string, frames []FrameResult) {
	if _, exists := w.results[name]; !exists {
		w.order = append(w.order, name)
	}
	w.results[name] = frames
	w.processed++
	w.lastError = ""
	for len(w.order) > w.opts.Keep {
		delete(w.results, w.order[0])
		w.order = w.order[1:]
	}
}

// analyse extracts frames from one segment with ffmpeg and runs the model on each.
func (w *Watcher) analyse(ctx context.Context, name string) ([]FrameResult, error) {
	inW, inH := w.model.InputSize()
	vf := fmt.Sprintf("fps=%g,scale=%d:%d:force_original_aspect_ratio=decrease", w.opts.FPS, inW, inH)
	cmd := exec.CommandContext(ctx, w.opts.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-threads", "1",
		"-i", filepath.Join(w.opts.HLSDir, name),
		"-vf", vf,
		"-c:v", "ppm", "-f", "image2pipe", "pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var (
		frames []FrameResult
		img    *image.RGBA
		r      = bufio.NewReaderSize(stdout, 1<<16)
	)
	for i := 0; ; i++ {
		img, err = readPPM(r, img)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("read frame: %w", err)
		}

		start := time.Now()
		dets, err := w.model.Detect(img, w.opts.MinScore)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}
		w.recordInference(time.Since(start))

		if !w.opts.AllClasses {
			dets = Persons(dets)
		}
		frames = append(frames, FrameResult{
			Offset:  float64(i) / w.opts.FPS,
			Objects: normalize(dets, img.Bounds()),
		})
	}

	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg: %s", msg)
	}
	return frames, nil
}

func (w *Watcher) recordInference(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	w.mu.Lock()
	if w.inferAvg == 0 {
		w.inferAvg = ms
	} else {
		w.inferAvg = w.inferAvg*0.9 + ms*0.1 // moving average
	}
	w.mu.Unlock()
}

func normalize(dets []Detection, b image.Rectangle) []Object {
	fw, fh := float32(b.Dx()), float32(b.Dy())
	out := make([]Object, 0, len(dets))
	for _, d := range dets {
		r := d.Box.Sub(b.Min)
		out = append(out, Object{
			Label: d.Label,
			Score: d.Score,
			X:     float32(r.Min.X) / fw,
			Y:     float32(r.Min.Y) / fh,
			W:     float32(r.Dx()) / fw,
			H:     float32(r.Dy()) / fh,
		})
	}
	return out
}

// readPlaylist returns the segment file names listed in an HLS media playlist.
func readPlaylist(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, filepath.Base(line))
	}
	return names, nil
}

// readPPM reads one binary PPM (P6) image, reusing dst when the size matches.
func readPPM(r *bufio.Reader, dst *image.RGBA) (*image.RGBA, error) {
	var magic string
	var w, h, maxVal int
	if _, err := fmt.Fscan(r, &magic, &w, &h, &maxVal); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}
	if magic != "P6" || maxVal != 255 || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("unsupported PPM header %s %dx%d max=%d", magic, w, h, maxVal)
	}
	if _, err := r.ReadByte(); err != nil { // single whitespace before pixel data
		return nil, err
	}

	if dst == nil || dst.Bounds().Dx() != w || dst.Bounds().Dy() != h {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	row := make([]byte, w*3)
	for y := 0; y < h; y++ {
		if _, err := io.ReadFull(r, row); err != nil {
			return nil, err
		}
		o := y * dst.Stride
		for x := 0; x < w; x++ {
			dst.Pix[o+x*4+0] = row[x*3+0]
			dst.Pix[o+x*4+1] = row[x*3+1]
			dst.Pix[o+x*4+2] = row[x*3+2]
			dst.Pix[o+x*4+3] = 255
		}
	}
	return dst, nil
}
