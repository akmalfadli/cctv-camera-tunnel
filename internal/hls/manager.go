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
	hlsRoot := m.config.HLSOutputRoot
	if hlsRoot == "" {
		return
	}

	threshold := time.Now().Add(-1 * time.Hour)
	_ = filepath.WalkDir(hlsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if info.ModTime().Before(threshold) {
			_ = os.Remove(path)
		}
		return nil
	})
}

func (m *Manager) GetStreamPath(cameraID string) string {
	return filepath.Join(m.config.HLSOutputRoot, cameraID, "stream.m3u8")
}

func (m *Manager) EnsureDirectory() error {
	return os.MkdirAll(m.config.HLSOutputRoot, 0755)
}
