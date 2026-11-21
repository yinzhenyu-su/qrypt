package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/control"
)

// coreMountCatalog adapts the namespace mount introspection to the command
// endpoint's capability contract. It reports configured mounts, including the
// ones that failed to initialize, because a client building a mount picker has
// to show those as unavailable rather than omit them.
type coreMountCatalog struct {
	core *Core
}

func (s coreMountCatalog) Mounts(context.Context) ([]control.CapabilityMount, error) {
	infos, err := s.core.MountsClient()
	if err != nil {
		return nil, err
	}
	out := make([]control.CapabilityMount, 0, len(infos))
	for _, info := range infos {
		out = append(out, control.CapabilityMount{
			Name:      info.Name,
			Path:      info.Path,
			Encrypted: info.Encrypted,
			State:     info.State,
			Error:     info.Error,
		})
	}
	return out, nil
}
