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
	if ok {
		cam.ID = id
	}
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

	cam.ID = id
	return r.config.UpsertCamera(cam)
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.config.DeleteCamera(id)
}
