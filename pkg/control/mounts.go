package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MountStatus is the transport view of one FUSE session hosted by a Core.
type MountStatus struct {
	ID         string    `json:"id"`
	Name       string    `json:"name,omitempty"`
	MountPoint string    `json:"mount_point"`
	ReadOnly   bool      `json:"read_only,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// MountAttachRequest asks the Core to mount one view of its filesystem.
type MountAttachRequest struct {
	Name       string `json:"name,omitempty"`
	MountPoint string `json:"mount_point"`
	ReadOnly   bool   `json:"read_only,omitempty"`
}

// MountService exposes FUSE session ownership over the command endpoint. It is
// separate from RuntimeService so read-only inspectors never gain mount
// authority.
type MountService interface {
	ListMounts(ctx context.Context) ([]MountStatus, error)
	AttachMount(ctx context.Context, req MountAttachRequest) (MountStatus, error)
	DetachMount(ctx context.Context, id string) error
}

// SetMountService installs the mount service before Start.
func (s *CommandServer) SetMountService(service MountService) {
	s.mounts = service
}

func (s *CommandServer) handleMounts(w http.ResponseWriter, r *http.Request) {
	if s.mounts == nil {
		http.Error(w, "mount service unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.mounts.ListMounts(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if items == nil {
			items = []MountStatus{}
		}
		writeJSON(w, map[string]any{"mounts": items})
	case http.MethodPost:
		var req MountAttachRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid mount request: "+err.Error(), http.StatusBadRequest)
			return
		}
		status, err := s.mounts.AttachMount(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, status)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *CommandServer) handleMountByID(w http.ResponseWriter, r *http.Request) {
	if s.mounts == nil {
		http.Error(w, "mount service unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/v1/control/mounts/"))
	if err != nil || id == "" {
		http.Error(w, "invalid mount id", http.StatusBadRequest)
		return
	}
	if err := s.mounts.DetachMount(r.Context(), id); err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

// ListMounts returns the FUSE sessions hosted by the Core behind this client.
func (c *Client) ListMounts(ctx context.Context) ([]MountStatus, error) {
	body, err := c.Get(ctx, "/v1/control/mounts")
	if err != nil {
		return nil, err
	}
	var response struct {
		Mounts []MountStatus `json:"mounts"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return response.Mounts, nil
}

// AttachMount asks the Core to host one more FUSE session.
func (c *Client) AttachMount(ctx context.Context, req MountAttachRequest) (MountStatus, error) {
	body, err := c.PostJSON(ctx, "/v1/control/mounts", req)
	if err != nil {
		return MountStatus{}, err
	}
	var status MountStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return MountStatus{}, err
	}
	return status, nil
}

// DetachMount unmounts a hosted session by id.
func (c *Client) DetachMount(ctx context.Context, id string) error {
	_, err := c.Delete(ctx, "/v1/control/mounts/"+url.PathEscape(id))
	return err
}
