package camera

import (
	"sync"
	
	"camera-tunnel/internal/config"
)

type Registry struct {
	mu     sync.RWMutex
	config *config.Config
}

func NewRegistry(cfg *config.Config) *Registry {
	return &Registry{
		config: cfg,
	}
}

func (r *Registry) Get(id string) (config.Camera, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cam, ok := r.config.Cameras[id]
	return cam, ok
}

func (r *Registry) GetAll() map[string]config.Camera {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	cams := make(map[string]config.Camera)
	for k, v := range r.config.Cameras {
		v.ID = k // Ensure ID is populated
		cams[k] = v
	}
	return cams
}

func (r *Registry) Set(id string, cam config.Camera) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.config.UpdateCamera(id, cam)
	return r.config.Save()
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.config.DeleteCamera(id)
	return r.config.Save()
}
