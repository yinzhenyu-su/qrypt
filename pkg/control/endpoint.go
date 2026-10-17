package control

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

type endpointInfo struct {
	network string
	address string
}

func parseEndpoint(endpoint string) (endpointInfo, error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "":
		return endpointInfo{}, fmt.Errorf("control: listen endpoint required")
	case strings.HasPrefix(endpoint, "unix:"):
		path := strings.TrimPrefix(endpoint, "unix:")
		if path == "" {
			return endpointInfo{}, fmt.Errorf("control: unix listen path required")
		}
		return endpointInfo{network: "unix", address: util.ExpandHome(path)}, nil
	case strings.HasPrefix(endpoint, "pipe:"):
		path := strings.TrimPrefix(endpoint, "pipe:")
		if path == "" {
			return endpointInfo{}, fmt.Errorf("control: pipe listen name required")
		}
		return endpointInfo{network: "pipe", address: path}, nil
	case strings.HasPrefix(endpoint, "tcp:"):
		return parseTCPListenEndpoint(strings.TrimPrefix(endpoint, "tcp:"))
	case strings.HasPrefix(endpoint, "http://"):
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return endpointInfo{}, err
		}
		return parseTCPListenEndpoint(parsed.Host)
	case strings.HasPrefix(endpoint, "https://"):
		return endpointInfo{}, fmt.Errorf("control: https listen is not supported")
	}
	if _, port, err := net.SplitHostPort(endpoint); err == nil && isNumericPort(port) {
		return parseTCPListenEndpoint(endpoint)
	}
	return endpointInfo{network: "unix", address: util.ExpandHome(endpoint)}, nil
}

func listenEndpoint(endpoint string) (network, address string, err error) {
	parsed, err := parseEndpoint(endpoint)
	if err != nil {
		return "", "", err
	}
	return parsed.network, parsed.address, nil
}

func listenEndpointNetwork(network, address string) (net.Listener, error) {
	if network == "tcp" {
		return net.Listen(network, address)
	}
	return listenLocalEndpoint(network, address)
}

func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	parsed, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if parsed.network == "tcp" {
		var dialer net.Dialer
		return dialer.DialContext(ctx, parsed.network, parsed.address)
	}
	return dialLocalEndpoint(ctx, parsed.network, parsed.address)
}

func parseTCPListenEndpoint(address string) (endpointInfo, error) {
	if address == "" {
		return endpointInfo{}, fmt.Errorf("control: tcp listen address required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return endpointInfo{}, err
	}
	if host == "" {
		return endpointInfo{}, fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return endpointInfo{}, fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	return endpointInfo{network: "tcp", address: address}, nil
}
