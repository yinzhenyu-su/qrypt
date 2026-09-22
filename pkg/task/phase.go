package task

// Phase (glossary: 阶段标签) is display-only progress wording. It never
// derives from State: the ban is on writing State strings or constants into
// a phase label, not on homonyms — display words declared here may share a
// spelling with a state word (e.g. failed). Names encode where a passthrough
// word comes from (Cloud…/Record…) so same-looking pairs stay distinguishable.
// The wire values are pinned by contract tests.
type Phase string

const (
	// PhasePending is work waiting to start (a queued item or record).
	PhasePending Phase = "pending"
	// PhaseScheduled is work scheduled for a future attempt.
	PhaseScheduled Phase = "scheduled"
	// PhaseRetrying is an exponential-backoff pause between attempts.
	PhaseRetrying Phase = "retrying"
	// PhaseReady (glossary: 等待取数) is data waiting for the app to read it.
	PhaseReady Phase = "ready"
	// PhaseAppStaging (glossary: 等待供数 in its paused form) is app-driven
	// staging of the bytes to transfer.
	PhaseAppStaging Phase = "staging"
	// PhaseServerStage is server-side staging before an upload (distinct
	// from PhaseAppStaging: the app stages the bytes there).
	PhaseServerStage Phase = "stage"
	// PhaseUpload is our transfer wording (distinct from PhaseCloudUploading,
	// the cloud drive's passthrough wording).
	PhaseUpload   Phase = "upload"
	PhaseDownload Phase = "download"
	PhaseMkdir    Phase = "mkdir"
	PhaseCopy     Phase = "copy"
	PhaseMove     Phase = "move"
	PhaseDelete   Phase = "delete"
	// PhaseDeleteSource is the source-cleanup tail of a copy-then-move.
	PhaseDeleteSource Phase = "delete_source"
	// PhaseSkipped is work that found nothing to do (already up to date).
	PhaseSkipped Phase = "skipped"
	// PhaseInstant is work satisfied without transferring bytes.
	PhaseInstant Phase = "instant"
	// PhaseDirect is direct (no staging) app streaming.
	PhaseDirect Phase = "direct"
	// PhaseQueuedUpload is staged work queued for its cloud upload.
	PhaseQueuedUpload Phase = "queued_upload"
	PhaseHashing      Phase = "hashing"
	// PhaseComplete is our terminal wording (distinct from
	// PhaseCloudCompleted, the cloud drive's passthrough wording).
	PhaseComplete Phase = "complete"
	// PhaseCloudUploading and PhaseCloudCompleted carry the cloud drive's
	// upload wording through to display.
	PhaseCloudUploading Phase = "uploading"
	PhaseCloudCompleted Phase = "completed"
	// PhaseRecordStarting and PhaseRecordSuperseded carry upload-record
	// wording through to display for records taken over before or during
	// their attempt.
	PhaseRecordStarting   Phase = "starting"
	PhaseRecordSuperseded Phase = "superseded"
	// PhasePartialFailed and PhaseFailed are display words that share a
	// spelling with state words; they are declared here, never derived.
	PhasePartialFailed Phase = "partial_failed"
	PhaseFailed        Phase = "failed"
	PhaseCanceled      Phase = "canceled"
)

// legacyPhase normalizes phase wording journaled by old code, which wrote
// State vocabulary strings as phase labels. The known leaks map to their
// display words; anything else passes through unchanged — independently
// declared display words may share a spelling with a state word (the ban is
// on deriving phase from State, not on homonyms).
func legacyPhase(s string) Phase {
	switch s {
	case "waiting_input":
		return PhaseAppStaging
	case "waiting_output":
		return PhaseReady
	case "retry_wait":
		return PhaseRetrying
	case "queued":
		return PhasePending
	default:
		return Phase(s)
	}
}
