package task

// Phase (glossary: 阶段标签) is display-only progress wording. It never
// derives from State: the two vocabularies are declared independently (some
// display words merely share a spelling with a state word, e.g. failed), and
// no code writes a State string into a phase label. The wire values are
// pinned by contract tests.
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
	// PhaseStaging (glossary: 等待供数 in its paused form) is app-driven
	// staging of the bytes to transfer.
	PhaseStaging Phase = "staging"
	// PhaseStage is server-side staging before an upload.
	PhaseStage    Phase = "stage"
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
	PhaseComplete     Phase = "complete"
	// PhaseUploading and PhaseCompleted carry the cloud drive's upload
	// wording through to display.
	PhaseUploading Phase = "uploading"
	PhaseCompleted Phase = "completed"
	// PhaseStarting and PhaseSuperseded carry upload-record wording through
	// to display for records taken over before or during their attempt.
	PhaseStarting   Phase = "starting"
	PhaseSuperseded Phase = "superseded"
	// PhasePartialFailed and PhaseFailed are display words that share a
	// spelling with state words; they are declared here, never derived.
	PhasePartialFailed Phase = "partial_failed"
	PhaseFailed        Phase = "failed"
	PhaseCanceled      Phase = "canceled"
)
