package contracttest

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Fixture owns a unique test directory on a driver. All debug test specs
// share it so the mkdir / list-verify / cleanup / residual-scan lifecycle
// lives in one place instead of being duplicated per test.
type Fixture struct {
	d      drive.Driver
	name   string
	rootID string
	// TestDir is the created test directory (valid after NewFixture).
	TestDir drive.Entry
}

// NewFixture creates a uniquely named test directory under the driver's
// root. The caller must Cleanup when done so no remote object is left behind.
func NewFixture(ctx context.Context, d drive.Driver, kind string) (*Fixture, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("generate test name: %w", err)
	}
	fx := &Fixture{
		d:      d,
		name:   fmt.Sprintf("__qrypt_%s_%x", kind, suffix),
		rootID: driverProbeRootID(ctx, d),
	}
	var err error
	fx.TestDir, err = d.Mkdir(ctx, fx.rootID, fx.name)
	if err != nil {
		return nil, fmt.Errorf("mkdir %q: %w", fx.name, err)
	}
	return fx, nil
}

// Name returns the unique test directory name (also the residual scan prefix).
func (fx *Fixture) Name() string { return fx.name }

// RootID returns the provider root id the test directory was created in.
func (fx *Fixture) RootID() string { return fx.rootID }

// VerifyList polls until name is (or is not) listed under parentID,
// tolerating backend eventual consistency with exponential backoff.
// When want is true it returns the matched entry.
func (fx *Fixture) VerifyList(ctx context.Context, parentID, name string, want bool) (drive.Entry, error) {
	const maxAttempts = 3
	delay := 1 * time.Second
	var lastEntries []drive.Entry
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		entries, err := fx.d.List(ctx, parentID)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("list %q: %w", parentID, err)
		}
		lastEntries = entries
		found := false
		for _, entry := range entries {
			if entry.Name == name {
				found = true
				if want {
					return entry, nil
				}
				break
			}
		}
		if !found && !want {
			return drive.Entry{}, nil
		}
	}
	if want {
		return drive.Entry{}, fmt.Errorf("entry %q not listed under %q after %d attempts: %v", name, parentID, maxAttempts, entryNames(lastEntries))
	}
	return drive.Entry{}, fmt.Errorf("entry %q still listed under %q after %d attempts: %v", name, parentID, maxAttempts, entryNames(lastEntries))
}

// Remove deletes one entry and reports whether cleanup succeeded.
func (fx *Fixture) Remove(ctx context.Context, entry drive.Entry, role string) (CRUDCleanupResult, bool) {
	start := time.Now()
	err := fx.d.Remove(ctx, entry)
	duration := time.Since(start)
	item := CRUDCleanupResult{
		Operation:  "remove",
		Role:       role,
		Name:       entry.Name,
		ID:         entry.ID,
		OK:         err == nil,
		Duration:   duration.String(),
		DurationMS: DurationMillis(duration),
	}
	if err != nil {
		item.Error = err.Error()
		item.ErrorCategory = drive.ErrorCategory(err)
	}
	return item, err == nil
}

// ScanResidual polls the root for entries matching the test prefix until
// none remain (cleanup fully propagated) or the poll budget is exhausted.
// It returns the leftover entries plus a per-attempt visibility timeline.
func (fx *Fixture) ScanResidual(ctx context.Context) ([]drive.Entry, []CRUDVisibilitySample, error) {
	const maxAttempts = 7
	delay := 1 * time.Second
	var residual []drive.Entry
	var timeline []CRUDVisibilitySample
	started := time.Now()
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		entries, err := fx.d.List(ctx, fx.rootID)
		if err != nil {
			timeline = append(timeline, CRUDVisibilitySample{
				Attempt:   attempt + 1,
				Elapsed:   time.Since(started).String(),
				ElapsedMS: DurationMillis(time.Since(started)),
				Error:     err.Error(),
			})
			return nil, timeline, fmt.Errorf("verify cleanup list: %w", err)
		}
		residual = residual[:0]
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, fx.name) {
				residual = append(residual, entry)
			}
		}
		elapsed := time.Since(started)
		timeline = append(timeline, CRUDVisibilitySample{
			Attempt:       attempt + 1,
			Elapsed:       elapsed.String(),
			ElapsedMS:     DurationMillis(elapsed),
			ResidualCount: len(residual),
			ResidualNames: residualNames(residual),
		})
		if len(residual) == 0 {
			return nil, timeline, nil
		}
	}
	return residual, timeline, fmt.Errorf("stale entries after cleanup: %v", entryNames(residual))
}
