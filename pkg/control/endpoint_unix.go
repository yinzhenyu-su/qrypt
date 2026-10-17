//go:build !windows

package control

import (
	"context"
	"fmt"
	"net"
)

func listenLocalEndpoint(network, address string) (net.Listener, error) {
	if network != "unix" {
		return nil, fmt.Errorf("control: %s endpoints are not supported on this platform", network)
	}
	return net.Listen("unix", address)
}

func dialLocalEndpoint(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "unix" {
		return nil, fmt.Errorf("control: %s endpoints are not supported on this platform", network)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", address)
}
