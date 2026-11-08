package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Config struct {
	Listen    string
	AuthToken string

	FFmpegPath string

	Input      string
	Camera     string
	InputCodec string
	VideoSize  string
	Framerate  int

	Encoder string
	Bitrate string

	HLSDir      string
	HLSTime     int
	HLSListSize int

	Detect DetectConfig

	AuthTokenGenerated bool
}

type DetectConfig struct {
	Enabled    bool
	Model      string
	Library    string
	FPS        float64
	MinScore   float64
	Threads    int
	AllClasses bool
}

func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}

	c := &Config{
		Listen:     env("LISTEN", "127.0.0.1:8080"),
		AuthToken:  env("AUTH_TOKEN", ""),
		FFmpegPath: env("FFMPEG_PATH", ""),
		Input:      env("INPUT", defaultInput()),
		Camera:     env("CAMERA", ""),
		InputCodec: env("INPUT_CODEC", "mjpeg"),
		VideoSize:  env("VIDEO_SIZE", "1280x720"),
		Encoder:    env("ENCODER", "libx264"),
		Bitrate:    env("BITRATE", "2500k"),
		HLSDir:     env("HLS_DIR", filepath.Join(os.TempDir(), "webcam-to-cctv", "hls")),
	}

	var err error
	if c.Framerate, err = envInt("FRAMERATE", 30); err != nil {
		return nil, err
	}
	if c.HLSTime, err = envInt("HLS_TIME", 2); err != nil {
		return nil, err
	}
	if c.HLSListSize, err = envInt("HLS_LIST_SIZE", 6); err != nil {
		return nil, err
	}

	c.Detect = DetectConfig{
		Enabled:    envBool("DETECT_ENABLED"),
		Model:      env("DETECT_MODEL", filepath.Join("models", "yolox_nano.onnx")),
		Library:    env("ONNXRUNTIME_PATH", defaultORTLib()),
		AllClasses: envBool("DETECT_ALL_CLASSES"),
	}
	if c.Detect.FPS, err = envFloat("DETECT_FPS", 2); err != nil {
		return nil, err
	}
	if c.Detect.MinScore, err = envFloat("DETECT_SCORE", 0.5); err != nil {
		return nil, err
	}
	if c.Detect.Threads, err = envInt("DETECT_THREADS", 2); err != nil {
		return nil, err
	}

	if c.AuthToken == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c.AuthToken = hex.EncodeToString(b)
		c.AuthTokenGenerated = true
	}

	return c, c.validate()
}

func (c *Config) validate() error {
	switch c.Input {
	case "dshow", "v4l2", "file":
		if c.Camera == "" {
			return fmt.Errorf("CAMERA is required for INPUT=%s", c.Input)
		}
	case "testsrc":
	default:
		return fmt.Errorf("invalid INPUT %q", c.Input)
	}
	if c.Framerate <= 0 || c.HLSTime <= 0 || c.HLSListSize <= 0 {
		return fmt.Errorf("FRAMERATE, HLS_TIME and HLS_LIST_SIZE must be > 0")
	}
	if c.Detect.Enabled && (c.Detect.FPS <= 0 || c.Detect.FPS > float64(c.Framerate)) {
		return fmt.Errorf("DETECT_FPS must be between 0 and FRAMERATE")
	}
	if len(c.AuthToken) < 12 {
		return fmt.Errorf("AUTH_TOKEN must be at least 12 characters")
	}
	return nil
}

func defaultInput() string {
	switch runtime.GOOS {
	case "windows":
		return "dshow"
	case "linux":
		return "v4l2"
	default:
		return "testsrc"
	}
}

func defaultORTLib() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join("models", "onnxruntime.dll")
	case "darwin":
		return filepath.Join("models", "libonnxruntime.dylib")
	default:
		return filepath.Join("models", "libonnxruntime.so")
	}
}

func envBool(key string) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envFloat(key string, def float64) (float64, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return f, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return n, nil
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}
