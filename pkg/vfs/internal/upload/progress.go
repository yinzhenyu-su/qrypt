package upload

import "github.com/yinzhenyu/qrypt/pkg/drive"

// observerProgress adapts the engine observer to drive.UploadProgress.
type observerProgress struct {
	observer Observer
	path     string
}

func (p observerProgress) Phase(phase drive.UploadPhase) {
	if p.path != "" && phase != "" {
		p.observer.State(p.path, string(phase))
	}
}

func (p observerProgress) Uploaded(n int64) {
	if p.path != "" && n > 0 {
		p.observer.Uploaded(p.path, int(n))
	}
}
