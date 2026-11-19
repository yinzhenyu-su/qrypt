package debug

import (
	"encoding/json"
	"io"
	"time"
)

// diagnosticsSchemaVersion versions the CLI envelope emitted by --json on the
// read-only debug commands. Bump it only for a breaking change; adding fields
// keeps the version.
const diagnosticsSchemaVersion = 1

// diagnosticsEnvelope wraps one raw debug-socket payload in a stable CLI
// shape. The socket response stays available under data, so scripts can read
// fields the envelope does not promote without waiting for the CLI contract to
// change.
type diagnosticsEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	Command       string          `json:"command"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Socket        string          `json:"socket,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// newDiagnosticsEnvelope builds the envelope for one socket response. It keeps
// the payload's own generated_at when present so a consumer can correlate the
// snapshot with the mount's clock instead of the CLI's.
func newDiagnosticsEnvelope(command, socket string, body []byte) diagnosticsEnvelope {
	envelope := diagnosticsEnvelope{
		SchemaVersion: diagnosticsSchemaVersion,
		Command:       command,
		GeneratedAt:   time.Now().UTC(),
		Socket:        socket,
	}
	if len(body) == 0 {
		return envelope
	}
	if json.Valid(body) {
		envelope.Data = append(json.RawMessage(nil), body...)
		var meta struct {
			GeneratedAt time.Time `json:"generated_at"`
		}
		if err := json.Unmarshal(body, &meta); err == nil && !meta.GeneratedAt.IsZero() {
			envelope.GeneratedAt = meta.GeneratedAt
		}
		return envelope
	}
	// A socket that returns plain text still produces valid JSON for scripts.
	if encoded, err := json.Marshal(string(body)); err == nil {
		envelope.Data = encoded
	}
	return envelope
}

func writeDiagnosticsEnvelope(out io.Writer, envelope diagnosticsEnvelope, pretty bool) error {
	encoder := json.NewEncoder(out)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(envelope)
}
