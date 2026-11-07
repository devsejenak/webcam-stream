# webcam-stream

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- [FFmpeg](https://ffmpeg.org/download.html), must be available in PATH:
  ```powershell
  winget install ffmpeg
  ffmpeg -version
  ```
- Camera permission: **Settings → Privacy & security → Camera → Let desktop apps access your camera** must be enabled

## Running

```powershell
git clone
cd webcam-stream
copy .env.example .env      # then fill in AUTH_TOKEN and CAMERA
go run main.go
```

Open http://localhost:8080 and enter your token. If `AUTH_TOKEN` is empty, a random token is printed to the terminal.

To build a binary: `go build -o webcam-stream.exe main.go`.

### Finding the camera name and modes

```powershell
ffmpeg -hide_banner -list_devices true -f dshow -i dummy
ffmpeg -hide_banner -f dshow -list_options true -i video="CAMERA NAME"
```

Pick a `vcodec=mjpeg` mode with the resolution and fps you want, then set `CAMERA`, `VIDEO_SIZE`, and `FRAMERATE` in `.env`. Avoid `pixel_format=yuyv422` (raw) modes; they usually drop to around 5 fps at higher resolutions.

## Configuration

Everything is configured through environment variables or `.env`. See `.env.example` for the full list.

| Variable | Default | Description |
|---|---|---|
| `AUTH_TOKEN` | random | Login token, at least 12 characters |
| `LISTEN` | `127.0.0.1:8080` | Server address |
| `INPUT` | `dshow` | `dshow`, `testsrc`, `v4l2` |
| `CAMERA` | – | dshow device name |
| `INPUT_CODEC` | `mjpeg` | Format delivered by the camera |
| `VIDEO_SIZE` | `1280x720` | Resolution |
| `FRAMERATE` | `30` | FPS |
| `ENCODER` | `libx264` | `h264_qsv`, `h264_nvenc`, `h264_amf` for hardware encoding |
| `BITRATE` | `2500k` | Maximum video bitrate |
| `HLS_TIME` | `2` | Segment duration (seconds) |
| `HLS_LIST_SIZE` | `6` | Number of segments in the playlist |
| `HLS_DIR` | `%TEMP%\webcam-stream\hls` | Segment folder |
| `FFMPEG_PATH` | `ffmpeg` | Path to ffmpeg if it is not in PATH |
| `DEBUG` | – | Set to `1` for verbose logging |

## Endpoints

| Route | Description |
|---|---|
| `GET /` | Viewer page |
| `GET,POST /login` | Login form |
| `POST /logout` | Log out |
| `GET /hls/stream.m3u8` | HLS playlist |
| `GET /hls/seg_*.ts` | Video segments |
| `GET /healthz` | ffmpeg status (JSON), returns 503 if the stream is not ready |

All routes except `/login` require authentication.