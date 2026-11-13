package uploadarch

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// ntpEpochOffset is the number of seconds between 1900-01-01 (NTP epoch) and
// 1970-01-01 (Unix epoch).
const ntpEpochOffset = 2208988800

// startSkewedNTPServer answers NTP requests with the system clock plus skew, so
// a client that syncs against it derives offset = skew, exactly like
// util.StartNTP does after a real sync. The client only reads the transmit
// timestamp, so that is all this server fills in.
func startSkewedNTPServer(t *testing.T, skew time.Duration) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 48)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 48 {
				continue
			}
			at := time.Now().Add(skew)
			resp := make([]byte, 48)
			resp[0] = 0x1c // LI=0, version 3, mode=server
			binary.BigEndian.PutUint32(resp[40:44], uint32(at.Unix()+ntpEpochOffset))
			binary.BigEndian.PutUint32(resp[44:48], uint32((uint64(at.Nanosecond())<<32)/uint64(time.Second)))
			_, _ = conn.WriteTo(resp, addr)
		}
	}()
	return conn.LocalAddr().String()
}

// setClockSkew makes util.Now() run skew ahead of the system clock, which is
// what a successful NTP sync produces when the local clock is off by -skew.
// skew == 0 puts the clock back to the system time. The returned function
// always restores the clock.
func setClockSkew(t *testing.T, skew time.Duration) func() {
	t.Helper()
	// StartNTP cancels any previous sync loop before applying the new config,
	// so this is also the reset path.
	reset := func() {
		util.StartNTP(context.Background(), util.NTPConfig{Enabled: false})
	}
	if skew == 0 {
		reset()
		return reset
	}
	addr := startSkewedNTPServer(t, skew)
	util.StartNTP(context.Background(), util.NTPConfig{
		Enabled:      true,
		Servers:      []string{addr},
		Timeout:      time.Second,
		PollInterval: time.Hour,
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := util.Now().Sub(time.Now())
		diff := got - skew
		if diff < 0 {
			diff = -diff
		}
		if diff < time.Second {
			return reset
		}
		if time.Now().After(deadline) {
			reset()
			t.Fatalf("NTP offset never applied: util.Now()-time.Now() = %s, want ~%s", got, skew)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
