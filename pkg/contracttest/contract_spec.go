package contracttest

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// runContractChecks runs the driver behavioral contract suite and assembles
// one TestRun: a single passing step when every check passes, or one failing
// step per violation.
// ParseXferSize parses the size query param for xfer tests. Accepts plain
// bytes, or binary suffixes: k/K (*1024), m/M (*1048576), g/G (*1073741824).
func ParseXferSize(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var multiplier int64 = 1
	last := value[len(value)-1]
	switch last {
	case 'k', 'K':
		multiplier = 1 << 10
		value = value[:len(value)-1]
	case 'm', 'M':
		multiplier = 1 << 20
		value = value[:len(value)-1]
	case 'g', 'G':
		multiplier = 1 << 30
		value = value[:len(value)-1]
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n * multiplier
}

func runContractChecks(ctx context.Context, mount string, d drive.Driver) TestRun {
	started := time.Now()
	violations := drive.RunBehaviorChecks(ctx, d)
	steps := make([]TestStep, 0, len(violations)+1)
	pass := true
	for _, v := range violations {
		pass = false
		steps = append(steps, TestStep{Operation: "contract", Name: v.Name, OK: false, Error: v.Err.Error()})
	}
	if pass {
		steps = append(steps, TestStep{Operation: "contract", Name: "behavior", OK: true})
	}
	finished := time.Now()
	return TestRun{
		Spec:       "contract",
		Mount:      mount,
		Pass:       pass,
		Steps:      steps,
		Started:    started,
		Finished:   finished,
		Duration:   finished.Sub(started).String(),
		DurationMS: finished.Sub(started).Milliseconds(),
	}
}
