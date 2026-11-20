package control

import (
	"context"
	"encoding/json"
)

// RuntimeStatus returns the identity and busy state of the Core behind this
// client.
func (c *Client) RuntimeStatus(ctx context.Context) (RuntimeStatus, error) {
	body, err := c.Get(ctx, "/v1/control/status")
	if err != nil {
		return RuntimeStatus{}, err
	}
	var status RuntimeStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return RuntimeStatus{}, err
	}
	return status, nil
}
