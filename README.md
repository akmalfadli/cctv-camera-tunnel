# CCTV Camera Tunnel - Refactored Architecture

This project has been refactored to a production-ready Go application for managing RTSP to HLS streaming for 1000+ viewers using a CDN-friendly architecture.

## Architecture

1.  **Ingest**: Go application (`cctv-control`) manages FFmpeg processes (one per camera) that ingest RTSP and output HLS segments.
2.  **Origin**: Nginx (configured via `nginx.conf`) serves the HLS segments from the disk. The Go app's built-in HTTP server is for control API and local testing only.
3.  **Delivery**: A CDN (Cloudflare, Akamai, etc.) pulls from Nginx and serves segments to end-users.

## Project Structure

- `cmd/cctv-control/`: Entry point (main.go).
- `internal/config/`: Configuration loading.
- `internal/camera/`: Camera registry.
- `internal/ffmpeg/`: Process supervisor for FFmpeg.
- `internal/hls/`: HLS path and cleanup management.
- `internal/api/`: HTTP API and viewer handlers.
- `internal/auth/`: JWT authentication.
- `internal/templates/`: HTML templates for the viewer and admin panel.
- `web/static/`: Static assets (JS/CSS).

## Admin & Management

The system includes a secure, web-based admin panel for managing cameras.

1.  **Access**: Navigate to `http://localhost:8080/login`.
2.  **Default Credentials**:
    - Username: `admin`
    - Password: `admin`
    - **Important**: Change these in `camera_config.json` immediately.
3.  **Features**:
    - **CRUD Operations**: Add, Edit, and Delete cameras directly from the UI.
    - **Live Control**: Enable or Disable streams instantly. The supervisor handles the background FFmpeg processes.
    - **Persistence**: All changes are automatically saved to `camera_config.json`.

## Deployment Guide

### 1. Build

```bash
go build -o cctv-control cmd/cctv-control/main.go
```

### 2. Configuration (`camera_config.json`)

The `camera_config.json` file controls server settings, authentication, and the camera list. It is automatically updated by the Admin Panel, but you can also edit it manually.

```json
{
  "server_port": 8080,
  "metrics_port": 0,
  "hls_output_root": "./hls",
  "segment_duration": 2,
  "playlist_size": 5,
  "cameras": {
    "cam1": {
      "name": "Front Door",
      "rtsp_url": "rtsp://user:pass@192.168.1.10:554/stream",
      "description": "Main Entrance",
      "enabled": true
    }
  }
}
```

### 3. Nginx Setup (Production Origin)

For production, do **not** rely on the Go app's built-in file server for high traffic. Install Nginx and use the provided `nginx.conf.example` as a template.

1.  Install Nginx.
2.  Copy `nginx.conf.example` logic to your Nginx config.
3.  Point the `alias` directive to the `hls_output_root` directory defined in your config (default: `./hls`).
4.  Ensure Nginx has read permissions for that directory.

### 4. Run

```bash
./cctv-control -config camera_config.json
```

### 5. Verify

- **Viewer**: Visit `http://localhost:8080` to see the public camera dashboard.
- **Admin**: Visit `http://localhost:8080/admin` to manage cameras.
- **Streams**: Available at `http://localhost:8080/hls/<camera_id>/stream.m3u8`.

## Performance Notes

- **FFmpeg**: Configured for `zerolatency`, `veryfast` preset, and 2-second segments. This ensures low latency (<5s) and CDN compatibility.
- **Go**: Acts only as a control plane (Control Plane). It does _not_ stream video bytes, ensuring it uses minimal CPU/RAM.
- **Scalability**: The `hls` folder contains static files (.m3u8 playlists and .ts segments). Nginx + CDN handles the massive read load, allowing for 1000+ concurrent viewers.

## CDN Integration Guide (Scaling to 1000+ Viewers)

To scale beyond 10-20 viewers, you **must** use a CDN (Content Delivery Network). This prevents your local internet upload speed from becoming the bottleneck.

### Option A: Cloudflare Tunnel (Recommended & Easiest)

This method exposes your local server directly to Cloudflare without needing a VPS or port forwarding.

1.  **Install `cloudflared`** on your local machine.
2.  **Authenticate**: `cloudflared tunnel login`
3.  **Create Tunnel**: `cloudflared tunnel create cctv-tunnel`
4.  **Configure**: Create `config.yml`:
    ```yaml
    tunnel: <Tunnel-UUID>
    credentials-file: /root/.cloudflared/<Tunnel-UUID>.json
    ingress:
      - hostname: cctv.yourdomain.com
        service: http://localhost:8081 # Point to your local Nginx port, NOT the Go app port
      - service: http_status:404
    ```
5.  **Run**: `cloudflared tunnel run cctv-tunnel`

### Option B: VPS Tunneling (Advanced)

If you prefer using your own VPS as the gateway:

1.  **Tunnel**: Use **FRP (Fast Reverse Proxy)** or **WireGuard** to tunnel traffic from Localhost:8081 -> VPS:80.
    - _Avoid SSH Remote Forwarding (`ssh -R`) for high-traffic video as it is TCP-over-TCP and inefficient._
2.  **DNS**: Point `cctv.yourdomain.com` to your VPS IP in Cloudflare DNS.
3.  **Proxy**: Enable the "Orange Cloud" (Proxied) in Cloudflare.

### Critical: Cloudflare Cache Rules

You must tell the CDN how to cache the files properly, otherwise streams will lag or not play.

1.  Go to **Cloudflare Dashboard > Caching > Cache Rules**.
2.  Create a rule: **"Cache HLS Segments"**
    - **If URL path ends with**: `.ts`
    - **Cache Status**: Eligible for Cache
    - **Edge Cache TTL**: 1 Month (or longer)
    - **Browser Cache TTL**: 1 Year
3.  Create a rule: **"Do NOT Cache Manifests"**
    - **If URL path ends with**: `.m3u8`
    - **Cache Status**: Bypass Cache (or set extremely low TTL like 2 seconds)

**Why?**

- `.ts` files are static video chunks. Once created, they never change. We want these served from Cloudflare's edge 99.9% of the time.
- `.m3u8` files are the "playlist". They update every 2 seconds with new segments. If cached, users will see an old playlist and the video will freeze.
