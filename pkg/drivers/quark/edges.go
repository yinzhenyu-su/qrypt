package quark

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	// edgePenaltyTTL keeps a bad CDN edge out of the dial candidates for a
	// while. Long enough to skip a failed edge, short enough that a recovered
	// one comes back.
	edgePenaltyTTL = 5 * time.Minute
	// downloadEdgeDialTimeout bounds one candidate edge's dial. Without it a
	// handful of blackholed addresses would serialize the whole dial budget.
	downloadEdgeDialTimeout = 5 * time.Second
)

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// edgeHealth remembers CDN edge addresses that recently timed out or stalled.
// The download transport asks it for dial candidates so the next request does
// not land on the same bad edge again.
type edgeHealth struct {
	ttl     time.Duration
	resolve func(context.Context, string) ([]netip.Addr, error)

	mu   sync.Mutex
	bad  map[string]edgePenalty
	next uint64
}

type edgePenalty struct {
	until  time.Time
	reason string
}

func newEdgeHealth(ttl time.Duration) *edgeHealth {
	if ttl <= 0 {
		ttl = edgePenaltyTTL
	}
	return &edgeHealth{ttl: ttl, bad: map[string]edgePenalty{}, resolve: lookupEdgeAddrs}
}

func lookupEdgeAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// penalize keeps one edge address out of the candidate set.
func (e *edgeHealth) penalize(ip, reason string) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.IsValid() {
		return
	}
	e.mu.Lock()
	e.bad[addr.String()] = edgePenalty{until: time.Now().Add(e.ttl), reason: reason}
	e.mu.Unlock()
	logging.L.Debugf("[QUARK] penalize edge=%s reason=%q ttl=%s", addr, reason, e.ttl)
}

// penalizeAddr is penalize for a "host:port" remote address.
func (e *edgeHealth) penalizeAddr(remoteAddr, reason string) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return
	}
	e.penalize(host, reason)
}

// hasPenalties reports whether at least one edge is still penalized.
func (e *edgeHealth) hasPenalties() bool {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for ip, penalty := range e.bad {
		if now.After(penalty.until) {
			delete(e.bad, ip)
		}
	}
	return len(e.bad) > 0
}

// debugSnapshot lists active penalties with their remaining lifetime.
func (e *edgeHealth) debugSnapshot(now time.Time) []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]map[string]any, 0, len(e.bad))
	for ip, penalty := range e.bad {
		if now.After(penalty.until) {
			delete(e.bad, ip)
			continue
		}
		out = append(out, map[string]any{
			"ip":            ip,
			"reason":        penalty.reason,
			"expires_in_ms": time.Until(penalty.until).Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["ip"].(string) < out[j]["ip"].(string) })
	return out
}

func (e *edgeHealth) isBad(addr netip.Addr, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	penalty, ok := e.bad[addr.String()]
	if !ok {
		return false
	}
	if now.After(penalty.until) {
		delete(e.bad, addr.String())
		return false
	}
	return true
}

// dial wraps base so that, while an edge is penalized, a hostname dial picks a
// non-penalized address from the host's current A/AAAA records. With no
// penalties it delegates unchanged, so normal resolution behaviour stays.
func (e *edgeHealth) dial(base dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if base == nil {
			return nil, errors.New("quark: download dialer is not configured")
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			return base(ctx, network, address)
		}
		if _, parseErr := netip.ParseAddr(host); parseErr == nil {
			return base(ctx, network, address)
		}
		if !e.hasPenalties() {
			return base(ctx, network, address)
		}
		addrs, err := e.resolve(ctx, host)
		if err != nil || len(addrs) == 0 {
			return base(ctx, network, address)
		}
		hasV4 := false
		for _, addr := range addrs {
			if addr.Is4() {
				hasV4 = true
				break
			}
		}
		e.mu.Lock()
		start := e.next
		e.next++
		e.mu.Unlock()
		now := time.Now()
		tried := 0
		var lastErr error
		for i := 0; i < len(addrs); i++ {
			addr := addrs[(int(start)+i)%len(addrs)]
			if !addr.IsValid() || (hasV4 && !addr.Is4()) {
				continue
			}
			if e.isBad(addr, now) {
				continue
			}
			tried++
			dialCtx, cancel := context.WithTimeout(ctx, downloadEdgeDialTimeout)
			conn, dialErr := base(dialCtx, network, net.JoinHostPort(addr.String(), port))
			cancel()
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
			var netErr net.Error
			if errors.As(dialErr, &netErr) && netErr.Timeout() {
				e.penalize(addr.String(), "dial timeout")
			}
		}
		if tried > 0 && lastErr != nil {
			return nil, lastErr
		}
		// Every candidate is penalized: fall back to normal resolution rather
		// than losing connectivity entirely.
		return base(ctx, network, address)
	}
}
