# CCTV Camera Tunnel

A production-ready Go application for managing RTSP to HLS streaming with **on-demand stream activation** — FFmpeg only runs when a viewer is watching, saving CPU and bandwidth.

## Architecture

```
RTSP Camera → FFmpeg (on-demand) → HLS Segments → Go HTTP Server → CDN → Viewers
```

1. **Ingest**: FFmpeg processes start automatically when a viewer requests a stream, and stop after 45 seconds of inactivity.
2. **Storage**: SQLite database (`cctv.db`) stores all settings, credentials, and camera configurations.
3. **Origin**: Built-in HTTP server serves HLS segments and admin UI. For production, place Nginx in front.
4. **Delivery**: CDN (Cloudflare, etc.) pulls from origin and scales to unlimited concurrent viewers.

## Features

- 🎥 **On-demand streaming** — FFmpeg starts on first HLS request, auto-stops when idle
- 🔐 **JWT authentication** with bcrypt password hashing and timing-safe login
- 🖥️ **Web admin panel** — add, edit, delete, toggle cameras in real time
- 📊 **Live status monitoring** — running / idle / error states per camera
- 🗄️ **SQLite-backed** persistent configuration
- 📡 **HLS streaming** with low-latency settings (2s segments, `veryfast`, `zerolatency`)
- 🌐 **CDN-friendly** — static `.ts` segments cacheable at the edge
- 🔑 **RTSP credentials via env vars** — never stored in plain text in the database

## Quick Start

### 1. Build

```bash
go build -o cctv-control ./cmd/cctv-control
```

### 2. Configure Environment

Copy the example and fill in your values:

```bash
cp .env.example .env
```

Edit `.env`:

```env
# Option A — plain password (hashed automatically on first run)
CCTV_ADMIN_PASSWORD=your_secure_password

# Option B — pre-hashed bcrypt password
# CCTV_ADMIN_PASSWORD_HASH=$2y$12$...your_bcrypt_hash...

# JWT signing secret
CCTV_JWT_SECRET=your_random_secret_string

# Per-camera RTSP credentials (reference by env var name in admin panel)
CAMERA_LOBBY_RTSP=rtsp://user:pass@192.168.1.10:554/stream1
```

Generate a JWT secret:
```bash
openssl rand -hex 32
```

Generate a bcrypt hash (optional):
```bash
htpasswd -bnBC 12 "" "your_password" | cut -d: -f2
```

> ⚠️ **Never commit `.env` to git.** It is listed in `.gitignore`. Only commit `.env.example`.

### 3. Run

```bash
./cctv-control -db cctv.db -env .env
```

### 4. Access

| Page | URL |
|------|-----|
| Login | `http://localhost:8080/login` |
| Admin Panel | `http://localhost:8080/admin` |
| Stream Viewer | `http://localhost:8080/view/<camera_id>` |
| HLS Playlist | `http://localhost:8080/hls/<camera_id>/stream.m3u8` |
| Stream Status | `http://localhost:8080/api/status` |
| Metrics | `http://localhost:8080/api/metrics` |

## On-Demand Streaming

Streams are **not** started at boot. The lifecycle is:

```
First HLS request arrives
    → FFmpeg process starts (~2–4s startup)
    → Segments appear, player begins playback
    → Each player poll keeps the stream alive
    → No requests for 45 seconds
    → FFmpeg stops automatically
    → Camera shows "idle" in admin panel
```

**Idle timeout** defaults to 45 seconds. Adjust it in `main.go`:
```go
streamMgr.SetIdleTimeout(60 * time.Second)
```

Stream states visible in admin:

| Badge | Meaning |
|-------|---------|
| 🟢 `running` | FFmpeg active, segments being produced |
| ⚪ `idle` | Camera enabled, no current viewers |
| 🔴 `error` | FFmpeg crashed, auto-retrying every 5s |
| — `stopped` | Camera disabled |

## Admin Panel

- **Add Camera** — name, RTSP URL (direct or via env var name), optional description
- **Edit Camera** — update any field; stream restarts automatically
- **Delete Camera** — removes camera and stops stream
- **Enable/Disable** — toggle per camera
- **Stream URLs** — direct links to HLS playlist and viewer page
- **Live Status** — auto-refreshes every 4 seconds

Each camera gets a unique ID (e.g., `cam_a1b2c3d4e5f6g7h8`) used in all stream URLs.

## RTSP Credentials via Environment Variables

Instead of storing RTSP URLs (with embedded passwords) in the database, reference an environment variable name:

1. Add to `.env`:
   ```env
   CAMERA_FRONT_DOOR_RTSP=rtsp://admin:secret@192.168.1.10:554/stream
   ```
2. In the admin panel **RTSP URL** field, enter the variable name: `CAMERA_FRONT_DOOR_RTSP`
3. The application resolves the actual URL at runtime from the environment.

## Command Line Options

```
-db string      Path to SQLite database (default "cctv.db")
-env string     Path to environment file (default ".env")
```

## Project Structure

```
├── cmd/cctv-control/    # Application entry point
├── internal/
│   ├── api/             # HTTP handlers and routing
│   ├── auth/            # JWT authentication
│   ├── camera/          # Camera registry
│   ├── config/          # SQLite-backed configuration
│   ├── ffmpeg/          # FFmpeg process supervisor (on-demand)
│   ├── hls/             # HLS file management & cleanup
│   └── templates/       # HTML templates (admin, viewer, login)
├── hls/                 # HLS output directory (auto-created)
├── cctv.db              # SQLite database (auto-created)
├── .env                 # Environment variables (DO NOT commit)
└── .env.example         # Safe template to commit
```

## Database Schema

| Table | Columns |
|-------|---------|
| `app_settings` | Server port, HLS settings, auth credentials |
| `cameras` | ID, name, description, RTSP URL/env, FPS, bitrate, GOP, enabled |
| `users` | Username, password hash, role |

## Production Deployment

### Nginx as HLS Origin

```nginx
location /hls/ {
    alias /path/to/cctv-camera-tunnel/hls/;
    add_header Access-Control-Allow-Origin *;

    location ~ \.m3u8$ {
        add_header Cache-Control "no-cache, no-store, must-revalidate";
        expires -1;
    }

    location ~ \.ts$ {
        add_header Cache-Control "public, max-age=31536000, immutable";
    }
}
```

### CDN Configuration (Cloudflare)

1. **Cache `.ts` segments**: Edge TTL 1 month, Browser TTL 1 year — they are immutable
2. **Bypass cache for `.m3u8`**: These change every 2 seconds

### Cloudflare Tunnel (no port forwarding required)

```bash
cloudflared tunnel create cctv
cloudflared tunnel run cctv
```

## API Endpoints

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/login` | ❌ | Authenticate, receive JWT cookie |
| GET | `/api/cameras` | ❌ | List cameras (public metadata only) |
| GET | `/api/status` | ✅ | Stream state per camera |
| GET | `/api/metrics` | ✅ | FFmpeg bitrate, restart count, last segment time |
| GET | `/api/admin/camera/get?id=` | ✅ | Get camera details |
| POST | `/api/admin/camera` | ✅ | Create camera |
| PUT | `/api/admin/camera` | ✅ | Update camera |
| DELETE | `/api/admin/camera?id=` | ✅ | Delete camera |
| POST | `/api/admin/camera/toggle` | ✅ | Enable / disable camera |
| GET | `/api/admin/settings` | ✅ | Get server settings |
| PUT | `/api/admin/settings` | ✅ | Update server settings |

## Performance Notes

- **On-demand**: Zero CPU/memory cost for cameras with no viewers
- **FFmpeg preset**: `veryfast` + `zerolatency` — optimized for low latency over quality
- **Latency**: Typically 4–6 seconds end-to-end (2s segments × playlist size)
- **Scalability**: HLS segments are static files; CDN handles all read load at scale

## License

MIT — Copyright (c) 2025 Akmal Fadli

See [LICENSE](./LICENSE) for full terms. You are free to use, modify, and sell this software with attribution.
