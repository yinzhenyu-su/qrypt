package driverutil

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// CredentialBinding owns the shared-session lifecycle for one driver: install,
// initial load, subscription, version checks, conflict recovery, coalesced
// refresh, and auth retry. The driver only supplies a payload decoder and an
// apply callback; the callback returns false when the payload is not usable
// (wrong account, expired, ...), in which case the version is not advanced.
type CredentialBinding[T any] struct {
	label  string
	decode func([]byte) (T, error)
	apply  func(value T, snapshot drive.CredentialSnapshot, source string) bool

	mu      sync.Mutex
	session drive.CredentialSession
	version uint64
	cancel  func()
}

// ApplySource values passed to the apply callback.
const (
	CredentialSourceState      = "state"
	CredentialSourceCredential = "credential"
)

func NewCredentialBinding[T any](label string, decode func([]byte) (T, error), apply func(T, drive.CredentialSnapshot, string) bool) *CredentialBinding[T] {
	return &CredentialBinding[T]{label: label, decode: decode, apply: apply}
}

func (b *CredentialBinding[T]) Install(session drive.CredentialSession) {
	b.mu.Lock()
	b.session = session
	b.mu.Unlock()
}

func (b *CredentialBinding[T]) Session() drive.CredentialSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session
}

func (b *CredentialBinding[T]) Version() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

// Snapshot returns the current shared-session snapshot (or an empty one).
func (b *CredentialBinding[T]) Snapshot() drive.CredentialSnapshot {
	session := b.Session()
	if session == nil {
		return drive.CredentialSnapshot{}
	}
	return session.Snapshot()
}

// Close stops the subscription and forgets the session.
func (b *CredentialBinding[T]) Close() {
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.session = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Load adopts the persisted payload during Init. It does not apply the version
// guard because the binding starts at version zero.
func (b *CredentialBinding[T]) Load() bool {
	session := b.Session()
	if session == nil {
		return false
	}
	snapshot := session.Snapshot()
	if len(snapshot.Payload) == 0 {
		return false
	}
	return b.applySnapshot(snapshot, CredentialSourceState, false)
}

// Subscribe follows commits from sibling mounts.
func (b *CredentialBinding[T]) Subscribe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == nil || b.cancel != nil {
		return
	}
	b.cancel = b.session.Subscribe(func(snapshot drive.CredentialSnapshot) {
		b.Apply(snapshot)
	})
}

// Apply adopts a snapshot when it is newer than the local view.
func (b *CredentialBinding[T]) Apply(snapshot drive.CredentialSnapshot) bool {
	if len(snapshot.Payload) == 0 {
		return false
	}
	return b.applySnapshot(snapshot, CredentialSourceCredential, true)
}

// Recover adopts the current session snapshot when it advanced past the local
// version. It reports whether a retry should use the new credentials.
func (b *CredentialBinding[T]) Recover() bool {
	session := b.Session()
	if session == nil {
		return false
	}
	return b.Apply(session.Snapshot())
}

// RetryOnAuth retries op once after adopting a newer shared snapshot.
func (b *CredentialBinding[T]) RetryOnAuth(isAuth func(error) bool, op func() error) error {
	if b.Session() == nil {
		return op()
	}
	err := op()
	if err == nil || isAuth == nil || !isAuth(err) {
		return err
	}
	if !b.Recover() {
		return err
	}
	return op()
}

// Commit publishes payload with the local version. A stale version is resolved
// by adopting the newer session value so the caller can inspect it.
func (b *CredentialBinding[T]) Commit(payload []byte, account string) error {
	session := b.Session()
	if session == nil {
		return nil
	}
	_, err := session.Update(b.Version(), payload, account)
	if errors.Is(err, drive.ErrCredentialVersionConflict) {
		b.Recover()
	}
	return err
}

// Replace overwrites the session with payload regardless of the binding's
// local version, for cases where the persisted payload cannot be decoded and
// must be healed (for example after a schema change).
func (b *CredentialBinding[T]) Replace(payload []byte, account string) error {
	session := b.Session()
	if session == nil {
		return nil
	}
	snapshot := session.Snapshot()
	if _, err := session.Update(snapshot.Version, payload, account); err != nil {
		return err
	}
	b.applySnapshot(session.Snapshot(), CredentialSourceCredential, false)
	return nil
}

// Refresh coalesces a provider refresh and applies the committed result.
func (b *CredentialBinding[T]) Refresh(ctx context.Context, fn func(value T, snapshot drive.CredentialSnapshot) ([]byte, string, error)) (drive.CredentialSnapshot, error) {
	session := b.Session()
	if session == nil {
		return drive.CredentialSnapshot{}, fmt.Errorf("%s: no shared credential session", b.label)
	}
	result, err := session.Refresh(ctx, func(current drive.CredentialSnapshot) ([]byte, string, error) {
		value, decodeErr := b.decode(current.Payload)
		if decodeErr != nil && len(current.Payload) > 0 {
			return nil, "", fmt.Errorf("%s: decode shared credential: %w", b.label, decodeErr)
		}
		return fn(value, current)
	})
	if err != nil {
		return result, err
	}
	b.Apply(result)
	return result, nil
}

// Debug adds the credential identity to a debug snapshot.
func (b *CredentialBinding[T]) Debug(extra map[string]any) {
	AppendCredentialDebug(extra, b.Session())
}

func (b *CredentialBinding[T]) applySnapshot(snapshot drive.CredentialSnapshot, source string, guard bool) bool {
	value, err := b.decode(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[%s] decode shared credential: %v", b.label, err)
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if guard && snapshot.Version <= b.version {
		return false
	}
	if !b.apply(value, snapshot, source) {
		return false
	}
	b.version = snapshot.Version
	return true
}
