# Multi-Camera CCTV Streaming System

A robust Go-based application that creates secure SSH tunnels to stream multiple RTSP cameras through a web interface. The system converts RTSP camera feeds to HTTP streams and makes them accessible via a VPS, providing remote monitoring capabilities.

## Features

- **Multiple Camera Support**: Stream multiple RTSP cameras simultaneously
- **SSH Tunnel Security**: Secure connection to remote VPS via SSH tunneling
- **Web Interface**: Clean HTML interface for viewing cameras
- **Real-time Streaming**: FFmpeg-powered video streaming with optimized settings
- **Auto-reconnection**: Automatic SSH tunnel reconnection on connection loss
- **RESTful API**: JSON API for camera management and integration
- **Template System**: Customizable HTML templates for the web interface
- **Fallback Authentication**: SSH agent and key file authentication support

## Architecture

```
Local Network (Cameras) → Go HTTP Server → SSH Tunnel → VPS → Internet
```

The system creates a local HTTP server that converts RTSP streams to HTTP, then establishes an SSH tunnel to make these streams accessible through a remote VPS.

## Prerequisites

### Required Software
- **Go 1.19+**: For building and running the application
- **FFmpeg**: For video stream processing
- **SSH Access**: To a VPS with SSH key authentication

### Installation Commands

**Ubuntu/Debian:**
```bash
sudo apt update
sudo apt install golang-go ffmpeg
```

**macOS:**
```bash
brew install go ffmpeg
```

**Windows:**
- Download Go from https://golang.org/
- Download FFmpeg from https://ffmpeg.org/

## Installation

1. **Clone or download the project**
```bash
git clone <repository-url>
cd camera-streaming-system
```

2. **Install Go dependencies**
```bash
go mod init camera-streaming
go get golang.org/x/crypto/ssh
go get golang.org/x/crypto/ssh/agent
go get golang.org/x/term
```

3. **Create templates directory**
```bash
mkdir templates
```

4. **Build the application**
```bash
go build -o camera-server main.go
```

## Configuration

### Initial Setup

1. **Run the application first time to generate config**
```bash
./camera-server
```

2. **Edit the generated `camera_config.json`**
```json
{
  "vps_host": "your-vps-domain.com",
  "vps_user": "your-username",
  "vps_port": 22,
  "ssh_key_path": "~/.ssh/id_rsa",
  "ssh_passphrase": "",
  "local_http_port": 8080,
  "vps_http_port": 8081,
  "cameras": {
    "camera1": {
      "name": "Front Door Camera",
      "rtsp_url": "rtsp://admin:password@192.168.1.100:554",
      "description": "Main entrance monitoring"
    },
    "camera2": {
      "name": "Garage Camera",
      "rtsp_url": "rtsp://admin:password@192.168.1.101:554",
      "description": "Garage area monitoring"
    }
  }
}
```

### Configuration Parameters

| Parameter | Description | Example |
|-----------|-------------|---------|
| `vps_host` | VPS domain or IP address | `"example.com"` |
| `vps_user` | SSH username for VPS | `"ubuntu"` |
| `vps_port` | SSH port (usually 22) | `22` |
| `ssh_key_path` | Path to SSH private key | `"~/.ssh/id_rsa"` |
| `ssh_passphrase` | SSH key passphrase (optional) | `""` |
| `local_http_port` | Local HTTP server port | `8080` |
| `vps_http_port` | Remote port for public access | `8081` |

### Camera Configuration

Each camera requires:
- **name**: Display name for the camera
- **rtsp_url**: Complete RTSP URL with credentials
- **description**: Optional description

## HTML Templates

Create these template files in the `templates/` directory:

### `templates/main_viewer.html`
```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Multi-Camera CCTV Viewer</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 0;
            padding: 20px;
            background-color: #f0f0f0;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        .header {
            text-align: center;
            margin-bottom: 30px;
        }
        .camera-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
            gap: 20px;
        }
        .camera-card {
            background: white;
            border-radius: 8px;
            padding: 15px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        .camera-title {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 10px;
        }
        .camera-video {
            width: 100%;
            height: 300px;
            border-radius: 4px;
            background-color: #000;
        }
        .camera-controls {
            margin-top: 10px;
            text-align: center;
        }
        .btn {
            background-color: #007bff;
            color: white;
            border: none;
            padding: 8px 16px;
            border-radius: 4px;
            cursor: pointer;
            margin: 0 5px;
        }
        .btn:hover {
            background-color: #0056b3;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>Multi-Camera CCTV System</h1>
            <p>{{ .CameraCount }} cameras online</p>
        </div>
        
        <div class="camera-grid">
            {{ range $id, $camera := .Cameras }}
            <div class="camera-card">
                <div class="camera-title">{{ $camera.Name }}</div>
                <video class="camera-video" controls autoplay muted>
                    <source src="/stream/{{ $id }}" type="video/mp4">
                    Your browser does not support video streaming.
                </video>
                <div class="camera-controls">
                    <button class="btn" onclick="location.href='/camera/{{ $id }}'">Full Screen</button>
                    <button class="btn" onclick="reloadStream('{{ $id }}')">Reload</button>
                </div>
                <p>{{ $camera.Description }}</p>
            </div>
            {{ end }}
        </div>
    </div>

    <script>
        function reloadStream(cameraId) {
            const video = document.querySelector(`[src="/stream/${cameraId}"]`).parentElement;
            const src = video.querySelector('source').src;
            video.load();
        }
        
        // Auto-reload streams every 5 minutes to prevent timeout
        setInterval(() => {
            document.querySelectorAll('.camera-video').forEach(video => {
                video.load();
            });
        }, 300000);
    </script>
</body>
</html>
```

### `templates/single_camera.html`
```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ .Camera.Name }} - CCTV Viewer</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 0;
            padding: 0;
            background-color: #000;
            color: white;
        }
        .container {
            padding: 20px;
        }
        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 20px;
        }
        .camera-video {
            width: 100%;
            height: 80vh;
            border-radius: 4px;
        }
        .controls {
            margin-top: 20px;
            text-align: center;
        }
        .btn {
            background-color: #007bff;
            color: white;
            border: none;
            padding: 10px 20px;
            border-radius: 4px;
            cursor: pointer;
            margin: 0 10px;
            font-size: 16px;
        }
        .btn:hover {
            background-color: #0056b3;
        }
        .back-btn {
            background-color: #6c757d;
        }
        .back-btn:hover {
            background-color: #545b62;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>{{ .Camera.Name }}</h1>
            <button class="btn back-btn" onclick="history.back()">← Back</button>
        </div>
        
        <video class="camera-video" controls autoplay muted>
            <source src="/stream/{{ .CameraID }}" type="video/mp4">
            Your browser does not support video streaming.
        </video>
        
        <div class="controls">
            <button class="btn" onclick="toggleFullscreen()">Full Screen</button>
            <button class="btn" onclick="reloadStream()">Reload Stream</button>
            <button class="btn" onclick="location.href='/'">All Cameras</button>
        </div>
        
        <p style="text-align: center; margin-top: 20px;">{{ .Camera.Description }}</p>
    </div>

    <script>
        function reloadStream() {
            document.querySelector('.camera-video').load();
        }
        
        function toggleFullscreen() {
            const video = document.querySelector('.camera-video');
            if (video.requestFullscreen) {
                video.requestFullscreen();
            }
        }
        
        // Auto-reload stream every 5 minutes
        setInterval(() => {
            document.querySelector('.camera-video').load();
        }, 300000);
    </script>
</body>
</html>
```

## Usage

### Starting the System

1. **Run the application**
```bash
./camera-server
```

2. **Monitor the logs**
The application will show:
- Camera connectivity tests
- HTTP server status
- SSH tunnel establishment
- Public access URLs

### Accessing Cameras

- **Main viewer**: `http://your-vps:8081`
- **Individual camera**: `http://your-vps:8081/camera/camera1`
- **API endpoint**: `http://your-vps:8081/api/cameras`
- **Direct stream**: `http://your-vps:8081/stream/camera1`

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Main multi-camera viewer |
| `/api/cameras` | GET | JSON list of all cameras |
| `/camera/{id}` | GET | Single camera full-screen view |
| `/stream/{id}` | GET | Direct video stream |

## Troubleshooting

### Common Issues

**1. FFmpeg not found**
```bash
# Ubuntu/Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg
```

**2. SSH connection failed**
- Verify SSH key permissions: `chmod 600 ~/.ssh/id_rsa`
- Test manual SSH connection: `ssh user@your-vps`
- Check VPS firewall settings

**3. Camera not accessible**
- Verify camera IP addresses
- Test RTSP URL with VLC media player
- Check network connectivity to cameras

**4. Port already in use**
- Change `local_http_port` in config
- Kill existing processes: `sudo lsof -t -i:8080 | xargs kill -9`

**5. Template not found**
- Ensure `templates/` directory exists
- Verify template files are present
- Check file permissions

### Debug Mode

Add verbose logging by modifying the logger configuration in the code or check the console output for detailed connection information.

### SSH Tunnel Manual Test

Test SSH tunnel manually:
```bash
ssh -i ~/.ssh/id_rsa -R 0.0.0.0:8081:localhost:8080 -N user@your-vps
```

## Security Considerations

- Use strong SSH keys and passphrases
- Regularly update SSH keys
- Configure VPS firewall to allow only necessary ports
- Use HTTPS reverse proxy for production deployment
- Regularly update camera firmware and change default passwords

## Performance Optimization

- **For high-resolution cameras**: Adjust FFmpeg settings in the code
- **For multiple cameras**: Consider increasing server resources
- **For remote access**: Use CDN or caching proxy
- **For mobile devices**: Implement adaptive bitrate streaming

## License

This project is provided as-is for educational and personal use. Please ensure compliance with local laws and regulations regarding surveillance systems.

## Support

For issues and questions:
1. Check the troubleshooting section
2. Verify configuration settings
3. Test individual components (SSH, cameras, FFmpeg)
4. Review application logs for error messages


For auto start the service put camera-tunnel.service to this path
/etc/systemd/system/camera-tunnel.service
