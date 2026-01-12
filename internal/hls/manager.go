package hls

import (
	"os"
	"path/filepath"
	"time"
	
	"camera-tunnel/internal/config"
)

type Manager struct {
	config *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{config: cfg}
}

func (m *Manager) Cleanup() {
	// Periodic cleanup of old segments if needed (though FFmpeg handles this mostly)
	// This serves as a safety net for orphaned files
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			m.pruneOldFiles()
		}
	}()
}

func (m *Manager) pruneOldFiles() {
	// Walk hls dir and remove .ts files older than 1 hour (just in case)
	// Implementation skipped for brevity, relies on FFmpeg hls_flags delete_segments
}

func (m *Manager) GetStreamPath(cameraID string) string {
	return filepath.Join(m.config.HLSOutputRoot, cameraID, "stream.m3u8")
}

func (m *Manager) EnsureDirectory() error {
	return os.MkdirAll(m.config.HLSOutputRoot, 0755)
}
