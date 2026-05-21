package camera

import (
	"camera-tunnel/internal/config"
)

type Registry struct {
	config *config.Config
}

func NewRegistry(cfg *config.Config) *Registry {
	return &Registry{
		config: cfg,
	}
}

func (r *Registry) Get(id string) (config.Camera, bool) {
	cam, ok := r.config.GetCamera(id)
	if ok {
		cam.ID = id
	}
	return cam, ok
}

func (r *Registry) GetAll() map[string]config.Camera {
	cams := r.config.GetCameras()
	for k, v := range cams {
		v.ID = k // Ensure ID is populated
		cams[k] = v
	}
	return cams
}

func (r *Registry) Set(id string, cam config.Camera) error {
	cam.ID = id
	return r.config.UpsertCamera(cam)
}

func (r *Registry) Delete(id string) error {
	return r.config.DeleteCamera(id)
}
