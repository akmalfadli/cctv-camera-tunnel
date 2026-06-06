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

// DefaultIdleTimeout is how long a stream stays alive with no HLS requests.
const DefaultIdleTimeout = 45 * time.Second

type StreamManager struct {
	config      *config.Config
	processes   map[string]*CameraProcess
	lastAccess  map[string]time.Time
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	idleTimeout time.Duration
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
	Terminated      chan struct{}
}

func NewStreamManager(cfg *config.Config) *StreamManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamManager{
		config:      cfg,
		processes:   make(map[string]*CameraProcess),
		lastAccess:  make(map[string]time.Time),
		ctx:         ctx,
		cancel:      cancel,
		idleTimeout: DefaultIdleTimeout,
	}
}

// SetIdleTimeout overrides the default idle timeout.
func (sm *StreamManager) SetIdleTimeout(d time.Duration) {
	sm.mu.Lock()
	sm.idleTimeout = d
	sm.mu.Unlock()
}

// StartIdleMonitor launches a background goroutine that stops streams
// that have had no HLS requests for longer than idleTimeout.
// Call this once from main after the server is set up.
func (sm *StreamManager) StartIdleMonitor() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sm.ctx.Done():
				return
			case <-ticker.C:
				sm.stopIdleStreams()
			}
		}
	}()
	log.Printf("[stream] Idle monitor started (timeout: %s)", sm.idleTimeout)
}

// stopIdleStreams finds and stops any running streams that haven't been
// accessed within the idle timeout window.
func (sm *StreamManager) stopIdleStreams() {
	sm.mu.RLock()
	threshold := time.Now().Add(-sm.idleTimeout)
	var toStop []string
	for id := range sm.processes {
		last, ok := sm.lastAccess[id]
		if !ok || last.Before(threshold) {
			toStop = append(toStop, id)
		}
	}
	sm.mu.RUnlock()

	for _, id := range toStop {
		log.Printf("[stream] Camera %s idle, stopping stream", id)
		sm.StopStream(id)
	}
}

// RequestStream records an HLS access for cameraID and starts the stream
// if it is not already running. Safe to call from multiple goroutines.
func (sm *StreamManager) RequestStream(id string) {
	// Update last access timestamp
	sm.mu.Lock()
	sm.lastAccess[id] = time.Now()
	_, alreadyRunning := sm.processes[id]
	sm.mu.Unlock()

	if alreadyRunning {
		return
	}

	// Start on-demand
	cam, ok := sm.config.GetCamera(id)
	if !ok || !cam.Enabled {
		return
	}
	sm.StartStream(id, cam)
}

func (sm *StreamManager) StartStream(id string, cam config.Camera) {
	sm.mu.Lock()
	if _, exists := sm.processes[id]; exists {
		sm.mu.Unlock()
		return // Already running
	}

	proc := &CameraProcess{
		CameraID:   id,
		Config:     cam,
		State:      StateStarting,
		Terminated: make(chan struct{}),
	}
	sm.processes[id] = proc
	sm.lastAccess[id] = time.Now() // Mark access so it isn't immediately idle
	sm.mu.Unlock()

	// Ensure output directory exists
	outputDir := filepath.Join(sm.config.HLSOutputRoot, id)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("[stream] Failed to create output dir for %s: %v", id, err)
		proc.mu.Lock()
		proc.State = StateError
		proc.LastError = err
		proc.mu.Unlock()
		return
	}

	go sm.runFFmpegLoop(proc, outputDir)
}

func (sm *StreamManager) runFFmpegLoop(proc *CameraProcess, outputDir string) {
	defer close(proc.Terminated)
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
				log.Printf("[stream] Camera %s missing RTSP credentials: %v", proc.CameraID, err)
				time.Sleep(5 * time.Second)
				continue
			}

			// Prepare context for this run
			ctx, cancel := context.WithCancel(sm.ctx)
			proc.mu.Lock()
			proc.cancel = cancel
			proc.mu.Unlock()

			segmentFilename := filepath.Join(outputDir, "segment_%03d.ts")
			playlistFilename := filepath.Join(outputDir, "stream.m3u8")

			fps := proc.Config.EffectiveFPS(30)
			gop := proc.Config.EffectiveGOP(fps * sm.config.SegmentDuration)

			// Build video codec args (bitrate must come before output path)
			videoArgs := []string{
				"-c:v", "libx264",
				"-preset", "veryfast",
				"-tune", "zerolatency",
				"-profile:v", "high",
				"-level", "4.0",
				"-g", fmt.Sprintf("%d", gop),
				"-sc_threshold", "0",
				"-r", fmt.Sprintf("%d", fps),
			}
			if proc.Config.Bitrate != "" {
				videoArgs = append(videoArgs, "-b:v", proc.Config.Bitrate)
			}

			args := []string{
				"-rtsp_transport", "tcp",
				"-i", rtspURL,
			}
			args = append(args, videoArgs...)
			args = append(args,
				// Audio
				"-c:a", "aac",
				"-b:a", "128k",
				"-ar", "44100",
				// HLS output
				"-f", "hls",
				"-hls_time", fmt.Sprintf("%d", sm.config.SegmentDuration),
				"-hls_list_size", fmt.Sprintf("%d", sm.config.PlaylistSize),
				"-hls_flags", "delete_segments+append_list+omit_endlist",
				"-hls_segment_type", "mpegts",
				"-hls_segment_filename", segmentFilename,
				playlistFilename,
			)

			cmd := exec.CommandContext(ctx, "ffmpeg", args...)
			// cmd.Stderr = os.Stderr // Uncomment for verbose FFmpeg logs

			proc.mu.Lock()
			proc.Cmd = cmd
			proc.State = StateRunning
			proc.LastError = nil
			proc.StartTime = time.Now()
			proc.mu.Unlock()

			segmentCtx, segmentCancel := context.WithCancel(ctx)
			go sm.pollSegments(segmentCtx, proc, outputDir)

			log.Printf("[stream] Starting FFmpeg for camera %s (on-demand)", proc.CameraID)
			err = cmd.Run()
			segmentCancel()

			proc.mu.Lock()
			if ctx.Err() != nil {
				// Context cancelled — intentional stop
				proc.State = StateStopped
				proc.mu.Unlock()
				return
			}

			// Unexpected exit — restart after delay
			proc.State = StateError
			proc.LastError = err
			proc.RestartCount++
			proc.mu.Unlock()

			log.Printf("[stream] FFmpeg for camera %s exited: %v. Restarting in 5s...", proc.CameraID, err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (sm *StreamManager) StopAll() {
	sm.cancel() // Cancel main context — signals all runFFmpegLoop goroutines to exit

	// Snapshot process list under lock, then release before waiting
	sm.mu.Lock()
	procs := make([]*CameraProcess, 0, len(sm.processes))
	for _, proc := range sm.processes {
		procs = append(procs, proc)
	}
	sm.mu.Unlock()

	var wg sync.WaitGroup
	for _, proc := range procs {
		wg.Add(1)
		go func(p *CameraProcess) {
			defer wg.Done()
			<-p.Terminated
		}(proc)
	}

	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()

	select {
	case <-c:
		log.Println("[stream] All camera processes stopped successfully.")
	case <-time.After(5 * time.Second):
		log.Println("[stream] WARNING: Timeout waiting for camera processes to stop.")
	}
}

func (sm *StreamManager) StopStream(id string) {
	sm.mu.Lock()
	proc, exists := sm.processes[id]
	if !exists {
		sm.mu.Unlock()
		return
	}
	delete(sm.processes, id)
	delete(sm.lastAccess, id)
	sm.mu.Unlock()

	proc.mu.Lock()
	if proc.cancel != nil {
		proc.cancel()
	}
	proc.State = StateStopped
	proc.mu.Unlock()

	select {
	case <-proc.Terminated:
		log.Printf("[stream] Camera %s cleanly stopped.", id)
	case <-time.After(5 * time.Second):
		log.Printf("[stream] WARNING: Timeout waiting for camera %s process to exit.", id)
	}
}

func (sm *StreamManager) RestartStream(id string, newConfig config.Camera) {
	sm.StopStream(id)
	sm.StartStream(id, newConfig)
}

func (sm *StreamManager) GetStatus() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := make(map[string]string)

	// Include all known cameras, not just running ones
	for id, cam := range sm.config.GetCameras() {
		if !cam.Enabled {
			status[id] = string(StateStopped)
			continue
		}
		proc, running := sm.processes[id]
		if !running {
			status[id] = "idle" // Enabled but not currently streaming
			continue
		}
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
	IdleSince       *time.Time   `json:"idle_since,omitempty"`
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
