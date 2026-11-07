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

	AuthTokenGenerated bool
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
		HLSDir:     env("HLS_DIR", filepath.Join(os.TempDir(), "webcam-stream", "hls")),
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
	case "dshow", "v4l2":
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
