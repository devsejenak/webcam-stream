# webcam-to-cctv

Self-hosted webcam CCTV in Go: live HLS viewer, on-device person detection
(YOLOX + ONNX Runtime), and Telegram snapshot alerts.

- **Live stream**: FFmpeg turns your webcam into an HLS stream you can watch in the browser, protected by a login token.
- **Person detection**: YOLOX runs locally on the CPU, so no video leaves your machine.
- **Telegram alerts**: get full-resolution snapshots when a person is detected, with a configurable cooldown to prevent spam.
- **Access from anywhere**: works with Tailscale Funnel or Cloudflare Tunnel.

Prebuilt Windows releases are on the [Releases](https://github.com/devsejenak/webcam-to-cctv/releases) page.

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- [FFmpeg](https://ffmpeg.org/download.html), must be available in PATH:
  ```powershell
  winget install ffmpeg
  ffmpeg -version
  ```
- For person detection: a C compiler for cgo (on Windows, MinGW-w64, e.g. via MSYS2) with `CGO_ENABLED=1`
  1. Install MSYS2:
     ```powershell
     winget install MSYS2.MSYS2
     ```
  2. Open the **MSYS2 UCRT64** terminal and install gcc:
     ```bash
     pacman -S mingw-w64-ucrt-x86_64-gcc
     ```
  3. Add `C:\msys64\ucrt64\bin` to your Windows `PATH`, then verify in PowerShell:
     ```powershell
     gcc --version
     go env -w CGO_ENABLED=1
     ```
- For person detection: the ONNX Runtime library, downloaded manually from the
  [ONNX Runtime v1.31.0 release](https://github.com/microsoft/onnxruntime/releases#release-v1.31.0)
  1. Download the archive for your platform. For Windows x64:
     [onnxruntime-win-x64-1.31.0.zip](https://github.com/microsoft/onnxruntime/releases/download/v1.31.0/onnxruntime-win-x64-1.31.0.zip)
  2. Extract it and copy `lib/onnxruntime.dll` into the project's `models/` folder, so the file is at `models/onnxruntime.dll`.

## Running

```powershell
git clone https://github.com/devsejenak/webcam-to-cctv.git
cd webcam-to-cctv
copy .env.example .env      # then fill in AUTH_TOKEN and CAMERA
go run main.go
```

Open http://localhost:8080 and enter your token. If `AUTH_TOKEN` is empty, a random token is printed to the terminal.

To build a binary: `go build -o webcam-to-cctv.exe main.go`.

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
| `HLS_DIR` | `%TEMP%\webcam-to-cctv\hls` | Segment folder |
| `FFMPEG_PATH` | `ffmpeg` | Path to ffmpeg if it is not in PATH |
| `DETECT_ENABLED` | `false` | Enable person detection |
| `DETECT_MODEL` | `models/yolox_nano.onnx` | YOLOX ONNX model |
| `ONNXRUNTIME_PATH` | `models/onnxruntime.dll` | ONNX Runtime library |
| `DETECT_FPS` | `2` | Frames analysed per second of video |
| `DETECT_SCORE` | `0.5` | Minimum confidence |
| `DETECT_THREADS` | `2` | CPU threads for ONNX Runtime |
| `DETECT_ALL_CLASSES` | `false` | Report all COCO classes, not only person |
| `TELEGRAM_BOT_TOKEN` | – | Bot token from @BotFather |
| `TELEGRAM_CHAT_ID` | – | Chat, group, or channel to send alerts to |
| `TELEGRAM_THREAD_ID` | – | Forum topic id in a group with topics |
| `ALERT_COOLDOWN` | `10m` | Minimum time between alerts (`30s`, `10m`, `1h`; a plain number is minutes) |
| `ALERT_PHOTOS` | `3` | Snapshots per alert (1–10), sent as an album |
| `DEBUG` | – | Set to `1` for verbose logging |

## Telegram alerts

With detection enabled, set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` to get a
Telegram message with full-resolution snapshots (person boxes drawn in red)
when a person is detected. After an alert, further detections are ignored for
`ALERT_COOLDOWN`; if sending fails, it is retried on the next detection after
one minute.

## Public access

Keep `LISTEN=127.0.0.1:8080` and expose the app with a tunnel so you can check the camera from any other device. Because the page becomes public, set a long random
`AUTH_TOKEN`.

1. Install a tunnel tool on the laptop (pick one):
   ```powershell
   winget install tailscale.tailscale             # Tailscale, then sign in once
   winget install --id Cloudflare.cloudflared     # Cloudflare, no account needed
   ```
2. Start the app (`go run main.go`), then start the tunnel in another terminal:
   ```powershell
   tailscale funnel 8080                           # Tailscale
   cloudflared tunnel --url http://127.0.0.1:8080  # Cloudflare
   ```
3. Open the HTTPS URL the tunnel prints on your other device and log in with your token.

| | Tailscale Funnel | Cloudflare quick tunnel |
|---|---|---|
| URL | `https://<laptop-name>.<tailnet>.ts.net`, stays the same | `https://<random>.trycloudflare.com`, changes on every restart |
| Account | Tailscale account; the first run prints a link to enable Funnel | None |
| Stop | `Ctrl+C` | `Ctrl+C` |

## Endpoints

| Route | Description |
|---|---|
| `GET /` | Viewer page |
| `GET,POST /login` | Login form |
| `POST /logout` | Log out |
| `GET /hls/stream.m3u8` | HLS playlist |
| `GET /hls/seg_*.ts` | Video segments |
| `GET /healthz` | ffmpeg status (JSON), returns 503 if the stream is not ready |
| `GET /api/detections` | Recent detection results per segment (JSON) |

All routes except `/login` require authentication.

## Credits

Person detection uses [YOLOX](https://github.com/Megvii-BaseDetection/YOLOX)
by Megvii (Apache-2.0), run through [ONNX Runtime](https://github.com/microsoft/onnxruntime)
via [onnxruntime_go](https://github.com/yalue/onnxruntime_go).

## License

[MIT](LICENSE). Third-party components keep their own licenses; see [models/THIRD_PARTY_NOTICES.md](models/THIRD_PARTY_NOTICES.md).
