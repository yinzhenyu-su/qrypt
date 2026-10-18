package task

import (
	"sort"
	"strings"
)

// BoundIdempotencyKeysDetailKey is the task detail key holding idempotency keys
// that were bound to a task after creation, mapped to the operation fingerprint
// of the request that bound each key. An active task can be returned for
// several explicit keys (it is the same source/parse/selection work); every key
// keeps its own request fingerprint so a later same-key request with different
// content conflicts, an identical request replays the same task, and a restart
// can restore the bindings from the journal. The primary key stays in
// Task.IdempotencyKey with Task.OperationFingerprint as its fingerprint.
const BoundIdempotencyKeysDetailKey = "idempotency_keys"

// BoundIdempotencyKeys returns a sorted copy of the extra keys bound to item.
func BoundIdempotencyKeys(item Task) []string {
	bindings := boundIdempotencyBindings(item)
	if len(bindings) == 0 {
		return nil
	}
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// IdempotencyKeyFingerprint resolves key to the operation fingerprint it was
// bound with. The primary key uses the task's own fingerprint.
func IdempotencyKeyFingerprint(item Task, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if item.IdempotencyKey == key {
		return item.OperationFingerprint, true
	}
	fingerprint, ok := boundIdempotencyBindings(item)[key]
	return fingerprint, ok
}

// BindIdempotencyKeyFingerprint binds key to item's content with the operation
// fingerprint of the request that supplied it. It returns false when the key is
// empty, already the primary key, or already bound (the existing binding is
// kept).
func BindIdempotencyKeyFingerprint(item *Task, key, fingerprint string) bool {
	if item == nil || key == "" || item.IdempotencyKey == key {
		return false
	}
	bindings := boundIdempotencyBindings(*item)
	if bindings == nil {
		bindings = map[string]string{}
	}
	if _, exists := bindings[key]; exists {
		return false
	}
	if item.Detail == nil {
		item.Detail = map[string]any{}
	}
	bindings[key] = fingerprint
	item.Detail[BoundIdempotencyKeysDetailKey] = bindings
	return true
}

// HasIdempotencyKey reports whether key is the task's primary key or one of
// its bound aliases.
func HasIdempotencyKey(item Task, key string) bool {
	_, ok := IdempotencyKeyFingerprint(item, key)
	return ok
}

// boundIdempotencyBindings reads the key -> fingerprint map. Older/journalled
// forms may still be a plain list of keys; those inherit the task's own
// fingerprint.
func boundIdempotencyBindings(item Task) map[string]string {
	if item.Detail == nil {
		return nil
	}
	value, ok := item.Detail[BoundIdempotencyKeysDetailKey]
	if !ok || value == nil {
		return nil
	}
	switch stored := value.(type) {
	case map[string]string:
		out := make(map[string]string, len(stored))
		for key, fingerprint := range stored {
			if key != "" {
				out[key] = fingerprint
			}
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(stored))
		for key, raw := range stored {
			fingerprint, _ := raw.(string)
			if key != "" {
				out[key] = fingerprint
			}
		}
		return out
	case []string:
		out := make(map[string]string, len(stored))
		for _, key := range stored {
			if key != "" {
				out[key] = item.OperationFingerprint
			}
		}
		return out
	case []any:
		out := make(map[string]string, len(stored))
		for _, raw := range stored {
			if key, ok := raw.(string); ok && key != "" {
				out[key] = item.OperationFingerprint
			}
		}
		return out
	default:
		return nil
	}
}

// idempotencyKeysDigest summarizes alias bindings for the persistent store's
// durable-change detection. Ordering is stable so map iteration cannot cause
// spurious journal writes.
func idempotencyKeysDigest(detail map[string]any) string {
	bindings := boundIdempotencyBindings(Task{Detail: detail})
	if len(bindings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"\x00"+bindings[key])
	}
	return strings.Join(parts, "\x01")
}
