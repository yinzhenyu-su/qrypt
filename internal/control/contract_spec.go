package control

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// runContractChecks runs the driver behavioral contract suite and assembles
// one TestRun: a single passing step when every check passes, or one failing
// step per violation.
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
