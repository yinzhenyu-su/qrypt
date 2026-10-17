package control

// NewProbeServer creates the active-probe transport. It uses the existing
// diagnostic handlers on a separate socket so probe writes cannot reach the
// task-control endpoint.
func NewProbeServer(endpoint string, source Snapshotter) (*Server, error) {
	server, err := NewServer(endpoint, source)
	if err != nil {
		return nil, err
	}
	server.mutable = true
	return server, nil
}
