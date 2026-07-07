package timeutil

import (
	"context"
	"encoding/binary"
	"net"
	"slices"
	"testing"
	"time"
)

func TestDefaultServersPreferDomesticNTP(t *testing.T) {
	if len(defaultServers) < 8 {
		t.Fatalf("expected domestic defaults plus global fallback, got %#v", defaultServers)
	}
	wantPrefix := []string{
		"ntp1.aliyun.com:123",
		"ntp2.aliyun.com:123",
		"ntp1.tencent.com:123",
		"ntp2.tencent.com:123",
		"ntp1.ntsc.ac.cn:123",
		"ntp2.ntsc.ac.cn:123",
		"ntp1.cstnet.cn:123",
		"0.cn.pool.ntp.org:123",
	}
	for i, want := range wantPrefix {
		if defaultServers[i] != want {
			t.Fatalf("default server %d = %q, want %q", i, defaultServers[i], want)
		}
	}
	if !slices.Contains(defaultServers, "time.cloudflare.com:123") {
		t.Fatal("expected Cloudflare NTP fallback")
	}
	if !slices.Contains(defaultServers, "time.google.com:123") {
		t.Fatal("expected Google NTP fallback")
	}
}

func TestQueryNTPUsesTransmitTimestamp(t *testing.T) {
	server, stop := startFakeNTPServer(t, time.Date(2026, 7, 7, 12, 34, 56, 123456789, time.UTC), false)
	defer stop()

	got, err := queryNTP(context.Background(), server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 7, 12, 34, 56, 123456788, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("queryNTP() = %s, want %s", got, want)
	}
}

func TestQueryNTPRejectsEmptyTransmitTimestamp(t *testing.T) {
	server, stop := startFakeNTPServer(t, time.Time{}, true)
	defer stop()

	if _, err := queryNTP(context.Background(), server, time.Second); err == nil {
		t.Fatal("expected empty transmit timestamp to fail")
	}
}

func TestClockStartSyncsFromNTPAndNowUsesOffset(t *testing.T) {
	remote := time.Now().Add(30 * time.Minute)
	server, stop := startFakeNTPServer(t, remote, false)
	defer stop()

	var c clock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.start(ctx, NTPConfig{
		Enabled:      true,
		Servers:      []string{server},
		Timeout:      time.Second,
		PollInterval: time.Hour,
	})
	defer c.stop()

	waitForCondition(t, time.Second, func() bool {
		return c.synced.Load()
	})
	if got := c.now(); got.Before(time.Now().Add(29 * time.Minute)) {
		t.Fatalf("clock now did not include NTP offset: got %s", got)
	}
	if c.lastSync.Load() == 0 {
		t.Fatal("expected last sync timestamp to be recorded")
	}
}

func TestClockStartDisabledClearsSyncState(t *testing.T) {
	var c clock
	c.synced.Store(true)
	c.offsetNS.Store(int64(time.Hour))
	c.lastSync.Store(time.Now().UnixNano())

	c.start(context.Background(), NTPConfig{Enabled: false})

	if c.synced.Load() {
		t.Fatal("expected disabled NTP to clear synced state")
	}
	if got := c.offsetNS.Load(); got != 0 {
		t.Fatalf("expected offset reset, got %d", got)
	}
	if got := c.lastSync.Load(); got != 0 {
		t.Fatalf("expected last sync reset, got %d", got)
	}
}

func TestClockSyncFallsBackWhenServersFail(t *testing.T) {
	var c clock
	c.syncOnce(context.Background(), []string{"", "127.0.0.1:1"}, time.Millisecond)
	if c.synced.Load() {
		t.Fatal("expected failed sync to leave clock unsynced")
	}
	if got := c.now(); time.Since(got) > time.Second {
		t.Fatalf("fallback system time looks stale: %s", got)
	}
}

func startFakeNTPServer(t *testing.T, transmit time.Time, emptyTransmit bool) (string, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 48)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			resp := make([]byte, 48)
			resp[0] = 0x1c
			if !emptyTransmit {
				secs := uint32(transmit.Unix() + ntpEpochOffset)
				frac := uint32((int64(transmit.Nanosecond()) << 32) / int64(time.Second))
				binary.BigEndian.PutUint32(resp[40:44], secs)
				binary.BigEndian.PutUint32(resp[44:48], frac)
			}
			_, _ = conn.WriteTo(resp, addr)
		}
	}()
	stop := func() {
		_ = conn.Close()
		<-done
	}
	return conn.LocalAddr().String(), stop
}

func waitForCondition(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
