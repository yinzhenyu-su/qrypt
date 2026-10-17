//go:build windows

package control

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// Restrict the default named pipe to the account that owns the Core process.
const defaultPipeSecurityDescriptor = "D:P(A;;GA;;;OW)"

func listenLocalEndpoint(network, address string) (net.Listener, error) {
	if network != "pipe" {
		return nil, fmt.Errorf("control: %s endpoints are not supported on Windows", network)
	}
	return winio.ListenPipe(address, &winio.PipeConfig{
		SecurityDescriptor: defaultPipeSecurityDescriptor,
	})
}

func dialLocalEndpoint(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "pipe" {
		return nil, fmt.Errorf("control: %s endpoints are not supported on Windows", network)
	}
	return winio.DialPipeContext(ctx, address)
}
