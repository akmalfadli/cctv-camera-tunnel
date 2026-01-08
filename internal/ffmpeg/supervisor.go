package ffmpeg

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"camera-tunnel/internal/config"
)

type ProcessState string

const (
	StateStopped  ProcessState = "stopped"
	StateStarting ProcessState = "starting"
	StateRunning  ProcessState = "running"
	StateError    ProcessState = "error"
)

type StreamManager struct {
	config    *config.Config
	processes map[string]*CameraProcess
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

type CameraProcess struct {
	CameraID        string
	Config          config.Camera
	Cmd             *exec.Cmd
	State           ProcessState
	LastError       error
	mu              sync.Mutex
	cancel          context.CancelFunc
	LastBitrateKbps float64
	LastSegmentAt   time.Time
	RestartCount    int
	StartTime       time.Time
}

func NewStreamManager(cfg *config.Config) *StreamManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamManager{
		config:    cfg,
		processes: make(map[string]*CameraProcess),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (sm *StreamManager) StartAll() {
	for id, cam := range sm.config.Cameras {
		if !cam.Enabled {
			continue
		}
		go sm.StartStream(id, cam)
	}
}

func (sm *StreamManager) StartStream(id string, cam config.Camera) {
	sm.mu.Lock()
	if _, exists := sm.processes[id]; exists {
		sm.mu.Unlock()
		return // Already running
	}

	proc := &CameraProcess{
		CameraID: id,
		Config:   cam,
		State:    StateStarting,
	}
	sm.processes[id] = proc
	sm.mu.Unlock()

	// Ensure output directory exists
	outputDir := filepath.Join(sm.config.HLSOutputRoot, id)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("Failed to create output dir for %s: %v", id, err)
		proc.mu.Lock()
		proc.State = StateError
		proc.LastError = err
		proc.mu.Unlock()
		return
	}

	go sm.runFFmpegLoop(proc, outputDir)
}

func (sm *StreamManager) runFFmpegLoop(proc *CameraProcess, outputDir string) {
	for {
		select {
		case <-sm.ctx.Done():
			return
		default:
			rtspURL, err := proc.Config.EffectiveRTSP()
			if err != nil {
				proc.mu.Lock()
				proc.State = StateError
				proc.LastError = err
				proc.mu.Unlock()
				log.Printf("Camera %s missing RTSP credentials: %v", proc.CameraID, err)
				time.Sleep(5 * time.Second)
				continue
			}

			// Prepare context for this run
			ctx, cancel := context.WithCancel(sm.ctx)
			proc.mu.Lock()
			proc.cancel = cancel
			proc.mu.Unlock()

			// Construct FFmpeg command
			// HLS output, H.264, AAC, Low Latency settings
			segmentFilename := filepath.Join(outputDir, "segment_%03d.ts")
			playlistFilename := filepath.Join(outputDir, "stream.m3u8")

			fps := proc.Config.EffectiveFPS(30)
			gop := proc.Config.EffectiveGOP(fps * sm.config.SegmentDuration)

			args := []string{
				"-rtsp_transport", "tcp",
				"-i", rtspURL,

				// Video Codec
				"-c:v", "libx264",
				"-preset", "veryfast",
				"-tune", "zerolatency",
				"-profile:v", "high",
				"-level", "4.0",

				// Keyframe alignment (crucial for HLS)
				"-g", fmt.Sprintf("%d", gop),
				"-sc_threshold", "0",
				"-r", fmt.Sprintf("%d", fps),

				// Audio Codec
				"-c:a", "aac",
				"-b:a", "128k",
				"-ar", "44100",

				// HLS Output options
				"-f", "hls",
				"-hls_time", fmt.Sprintf("%d", sm.config.SegmentDuration),
				"-hls_list_size", fmt.Sprintf("%d", sm.config.PlaylistSize),
				"-hls_flags", "delete_segments+append_list+omit_endlist",
				"-hls_segment_type", "mpegts",
				"-hls_segment_filename", segmentFilename,
				playlistFilename,
			}

			if proc.Config.Bitrate != "" {
				args = append(args, "-b:v", proc.Config.Bitrate)
			}

			cmd := exec.CommandContext(ctx, "ffmpeg", args...)

			// Capture stderr for debugging
			// cmd.Stderr = os.Stderr // Uncomment for verbose logs

			proc.mu.Lock()
			proc.Cmd = cmd
			proc.State = StateRunning
			proc.LastError = nil
			proc.StartTime = time.Now()
			proc.RestartCount++
			proc.mu.Unlock()

			segmentCtx, segmentCancel := context.WithCancel(ctx)
			go sm.pollSegments(segmentCtx, proc, outputDir)

			log.Printf("Starting FFmpeg for camera %s", proc.CameraID)
			err = cmd.Run()
			segmentCancel()

			proc.mu.Lock()
			if ctx.Err() != nil {
				// Context cancelled (Shutdown)
				proc.State = StateStopped
				proc.mu.Unlock()
				return
			}

			// Unexpected exit
			proc.State = StateError
			proc.LastError = err
			proc.mu.Unlock()

			log.Printf("FFmpeg for camera %s exited: %v. Restarting in 5s...", proc.CameraID, err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (sm *StreamManager) StopAll() {
	sm.cancel() // Cancel main context
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Wait for processes? In a real supervisor we might waitgroup here.
	// But context cancellation should kill the exec.CommandContext
}

func (sm *StreamManager) StopStream(id string) {
	sm.mu.Lock()
	proc, exists := sm.processes[id]
	if !exists {
		sm.mu.Unlock()
		return
	}
	delete(sm.processes, id)
	sm.mu.Unlock()

	// Stop the specific process loop
	proc.mu.Lock()
	if proc.cancel != nil {
		proc.cancel()
	}
	proc.State = StateStopped
	proc.mu.Unlock()

	// Wait a bit or ensure the loop exits?
	// The cancel() call triggers ctx.Done() in runFFmpegLoop, causing it to return.
}

func (sm *StreamManager) RestartStream(id string, newConfig config.Camera) {
	sm.StopStream(id)
	time.Sleep(1 * time.Second) // Give it a moment to cleanup
	sm.StartStream(id, newConfig)
}

func (sm *StreamManager) GetStatus() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := make(map[string]string)
	for id, proc := range sm.processes {
		proc.mu.Lock()
		status[id] = string(proc.State)
		proc.mu.Unlock()
	}
	return status
}

type CameraMetrics struct {
	State           ProcessState `json:"state"`
	LastError       string       `json:"last_error,omitempty"`
	LastBitrateKbps float64      `json:"bitrate_kbps"`
	LastSegmentAt   time.Time    `json:"last_segment_at"`
	RestartCount    int          `json:"restart_count"`
}

func (sm *StreamManager) GetMetrics() map[string]CameraMetrics {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	metrics := make(map[string]CameraMetrics, len(sm.processes))
	for id, proc := range sm.processes {
		proc.mu.Lock()
		lastErr := ""
		if proc.LastError != nil {
			lastErr = proc.LastError.Error()
		}
		metrics[id] = CameraMetrics{
			State:           proc.State,
			LastError:       lastErr,
			LastBitrateKbps: proc.LastBitrateKbps,
			LastSegmentAt:   proc.LastSegmentAt,
			RestartCount:    proc.RestartCount,
		}
		proc.mu.Unlock()
	}
	return metrics
}

func (sm *StreamManager) pollSegments(ctx context.Context, proc *CameraProcess, outputDir string) {
	segmentSeconds := float64(sm.config.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = 2
	}
	ticker := time.NewTicker(time.Duration(segmentSeconds * float64(time.Second)))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fi, err := latestSegmentInfo(outputDir)
			if err != nil || fi == nil {
				continue
			}
			bitrate := (float64(fi.Size()) * 8) / segmentSeconds / 1000
			proc.mu.Lock()
			proc.LastSegmentAt = fi.ModTime()
			proc.LastBitrateKbps = bitrate
			proc.mu.Unlock()
		}
	}
}

func latestSegmentInfo(dir string) (os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var latest os.FileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ts") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if latest == nil || info.ModTime().After(latest.ModTime()) {
			latest = info
		}
	}
	return latest, nil
}
