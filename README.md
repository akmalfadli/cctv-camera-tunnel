# CCTV Camera Tunnel

A production-ready Go application for managing RTSP to HLS streaming, supporting 1000+ concurrent viewers via CDN-friendly architecture.

## Architecture

1. **Ingest**: Go application (`cctv-control`) manages FFmpeg processes (one per camera) that convert RTSP streams to HLS segments.
2. **Storage**: SQLite database (`cctv.db`) stores all settings, credentials, and camera configurations.
3. **Origin**: Built-in HTTP server serves HLS segments and admin UI. For production, use Nginx as origin server.
4. **Delivery**: CDN (Cloudflare, Akamai, etc.) pulls from origin and serves to end-users.

## Features

- Web-based admin panel for camera management
- JWT authentication with bcrypt password hashing
- Automatic unique ID generation for cameras
- Real-time stream status monitoring
- SQLite-backed persistent configuration
- HLS streaming with low-latency settings
- Metrics endpoint for monitoring

## Quick Start

### 1. Build

```bash
go build -o cctv-control ./cmd/cctv-control
```

### 2. Configure Environment

Create a `.env` file:

```env
CCTV_ADMIN_PASSWORD=your_secure_password
CCTV_JWT_SECRET=your_random_secret_string
```

Or use a pre-hashed password:

```env
CCTV_ADMIN_PASSWORD_HASH=$2y$12$...your_bcrypt_hash...
CCTV_JWT_SECRET=your_random_secret_string
```

Generate a bcrypt hash:
```bash
htpasswd -bnBC 12 "" "your_password" | cut -d: -f2
```

Generate a JWT secret:
```bash
openssl rand -hex 32
```

### 3. Run

```bash
./cctv-control -db cctv.db -env .env
```

### 4. Access

- **Login**: `http://localhost:8080/login`
- **Admin Panel**: `http://localhost:8080/admin`
- **Stream Viewer**: `http://localhost:8080/view/<camera_id>`
- **HLS Stream**: `http://localhost:8080/hls/<camera_id>/stream.m3u8`
- **Metrics**: `http://localhost:8080/api/metrics`

## Admin Panel

The admin panel provides:

- **Add Camera**: Enter name, RTSP URL, and optional description
- **Edit Camera**: Modify existing camera settings
- **Delete Camera**: Remove camera and stop its stream
- **Enable/Disable**: Toggle streaming on/off per camera
- **Stream URLs**: Direct links to HLS streams and viewer pages
- **Live Status**: Real-time FFmpeg process status

Each camera gets a unique ID (e.g., `cam_a1b2c3d4e5f6g7h8`) used in stream URLs.

## Command Line Options

```
-db string      Path to SQLite database (default "cctv.db")
-config string  Path to legacy JSON config for initial seeding (default "camera_config.json")
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
│   ├── ffmpeg/          # FFmpeg process supervisor
│   ├── hls/             # HLS file management
│   └── templates/       # HTML templates
├── hls/                 # HLS output directory
├── cctv.db              # SQLite database
└── .env                 # Environment variables
```

## Database Schema

Settings and cameras are stored in SQLite:

- `app_settings`: Server port, metrics port, HLS settings, auth credentials
- `cameras`: ID, name, description, RTSP URL, enabled status

## Production Deployment

### Nginx Setup

For high traffic, use Nginx as the HLS origin:

```nginx
location /hls/ {
    alias /path/to/hls/;
    add_header Cache-Control no-cache;
    add_header Access-Control-Allow-Origin *;
    types {
        application/vnd.apple.mpegurl m3u8;
        video/mp2t ts;
    }
}
```

### CDN Configuration (Cloudflare)

1. **Cache `.ts` files**: Edge TTL 1 month, Browser TTL 1 year
2. **Bypass cache for `.m3u8`**: These update every 2 seconds

### Cloudflare Tunnel

Expose local server without port forwarding:

```bash
cloudflared tunnel create cctv
cloudflared tunnel run cctv
```

## Performance Notes

- **FFmpeg**: Uses `veryfast` preset, `zerolatency` tune, 2-second segments
- **Latency**: Typically under 5 seconds end-to-end
- **Scalability**: HLS segments are static files; CDN handles read load

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/login` | Authenticate and get JWT |
| GET | `/api/cameras` | List cameras (public) |
| GET | `/api/status` | Stream status per camera |
| GET | `/api/metrics` | FFmpeg process metrics |
| GET | `/api/admin/camera/get?id=` | Get camera details |
| POST | `/api/admin/camera` | Create camera |
| PUT | `/api/admin/camera` | Update camera |
| DELETE | `/api/admin/camera?id=` | Delete camera |
| POST | `/api/admin/camera/toggle` | Enable/disable camera |

## License

MIT
