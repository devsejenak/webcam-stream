package transcoder

import (
	"fmt"
	"path/filepath"
	"strconv"

	"webcam-to-cctv/stream/config"
)

const (
	PlaylistName   = "stream.m3u8"
	segmentPattern = "seg_%05d.ts"
)

func BuildArgs(c *config.Config) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostats"}
	args = append(args, inputArgs(c)...)
	args = append(args, encoderArgs(c)...)

	gop := strconv.Itoa(c.Framerate * c.HLSTime)
	args = append(args,
		"-g", gop, "-keyint_min", gop, "-sc_threshold", "0",
		"-an",
		"-f", "hls",
		"-hls_time", strconv.Itoa(c.HLSTime),
		"-hls_list_size", strconv.Itoa(c.HLSListSize),
		"-hls_flags", "delete_segments+omit_endlist+independent_segments+temp_file",
		"-hls_segment_filename", filepath.Join(c.HLSDir, segmentPattern),
		filepath.Join(c.HLSDir, PlaylistName),
	)
	return args
}

func inputArgs(c *config.Config) []string {
	fps := strconv.Itoa(c.Framerate)
	switch c.Input {
	case "dshow":
		a := []string{"-f", "dshow", "-rtbufsize", "100M"}
		if c.InputCodec != "" {
			a = append(a, "-vcodec", c.InputCodec)
		}
		return append(a, "-video_size", c.VideoSize, "-framerate", fps, "-i", "video="+c.Camera)
	case "v4l2":
		a := []string{"-f", "v4l2"}
		if c.InputCodec != "" {
			a = append(a, "-input_format", c.InputCodec)
		}
		return append(a, "-video_size", c.VideoSize, "-framerate", fps, "-i", c.Camera)
	default:
		src := fmt.Sprintf("testsrc2=size=%s:rate=%s", c.VideoSize, fps)
		return []string{"-re", "-f", "lavfi", "-i", src}
	}
}

func encoderArgs(c *config.Config) []string {
	rate := []string{"-b:v", c.Bitrate, "-maxrate", c.Bitrate, "-bufsize", doubleRate(c.Bitrate)}
	switch c.Encoder {
	case "libx264":
		return append([]string{"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p"}, rate...)
	default:
		return append([]string{"-c:v", c.Encoder, "-pix_fmt", "nv12"}, rate...)
	}
}

func doubleRate(r string) string {
	if n := len(r); n > 1 {
		if v, err := strconv.Atoi(r[:n-1]); err == nil {
			return strconv.Itoa(v*2) + r[n-1:]
		}
	}
	if v, err := strconv.Atoi(r); err == nil {
		return strconv.Itoa(v * 2)
	}
	return r
}
