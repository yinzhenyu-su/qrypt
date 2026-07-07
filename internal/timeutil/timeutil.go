package timeutil

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTimeout      = 1500 * time.Millisecond
	defaultPollInterval = 30 * time.Minute
	ntpEpochOffset      = 2208988800
)

var defaultServers = []string{
	"ntp1.aliyun.com:123",
	"ntp2.aliyun.com:123",
	"ntp1.tencent.com:123",
	"ntp2.tencent.com:123",
	"ntp1.ntsc.ac.cn:123",
	"ntp2.ntsc.ac.cn:123",
	"ntp1.cstnet.cn:123",
	"0.cn.pool.ntp.org:123",
	"time.cloudflare.com:123",
	"time.google.com:123",
}

var globalClock = &clock{}

type clock struct {
	startMu sync.Mutex
	cancel  context.CancelFunc

	offsetNS atomic.Int64
	synced   atomic.Bool
	lastSync atomic.Int64
}

type NTPConfig struct {
	Enabled      bool
	Servers      []string
	Timeout      time.Duration
	PollInterval time.Duration
}

type Status struct {
	Synced     bool      `json:"synced"`
	LastSync   time.Time `json:"last_sync,omitempty"`
	Offset     string    `json:"offset,omitempty"`
	Server     string    `json:"server,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	UsingNTP   bool      `json:"using_ntp"`
	SystemTime time.Time `json:"system_time"`
}

func Now() time.Time {
	return globalClock.now()
}

func StartNTP(parent context.Context, cfg NTPConfig) {
	globalClock.start(parent, cfg)
}

func StopNTP() {
	globalClock.stop()
}

func (c *clock) now() time.Time {
	now := time.Now()
	if !c.synced.Load() {
		return now
	}
	return now.Add(time.Duration(c.offsetNS.Load()))
}

func (c *clock) start(parent context.Context, cfg NTPConfig) {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if !cfg.Enabled {
		c.synced.Store(false)
		c.offsetNS.Store(0)
		c.lastSync.Store(0)
		return
	}
	servers := cfg.Servers
	if len(servers) == 0 {
		servers = defaultServers
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	go c.syncLoop(ctx, servers, timeout, poll)
}

func (c *clock) stop() {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (c *clock) syncLoop(ctx context.Context, servers []string, timeout, poll time.Duration) {
	c.syncOnce(ctx, servers, timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.syncOnce(ctx, servers, timeout)
		}
	}
}

func (c *clock) syncOnce(ctx context.Context, servers []string, timeout time.Duration) {
	for _, server := range servers {
		remote, err := queryNTP(ctx, server, timeout)
		if err != nil {
			continue
		}
		now := time.Now()
		c.offsetNS.Store(remote.Sub(now).Nanoseconds())
		c.lastSync.Store(now.UnixNano())
		c.synced.Store(true)
		return
	}
}

func queryNTP(ctx context.Context, server string, timeout time.Duration) (time.Time, error) {
	if server == "" {
		return time.Time{}, errors.New("empty NTP server")
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	req := make([]byte, 48)
	req[0] = 0x1b
	if _, err := conn.Write(req); err != nil {
		return time.Time{}, err
	}
	resp := make([]byte, 48)
	if _, err := conn.Read(resp); err != nil {
		return time.Time{}, err
	}
	secs := binary.BigEndian.Uint32(resp[40:44])
	frac := binary.BigEndian.Uint32(resp[44:48])
	if secs == 0 {
		return time.Time{}, errors.New("empty NTP transmit timestamp")
	}
	nsec := (int64(frac) * int64(time.Second)) >> 32
	return time.Unix(int64(secs)-ntpEpochOffset, nsec).UTC(), nil
}
