package task

// RetryStrategy selects how a task of a type is retried when the caller asks to
// retry it. The strategies are ordered by how much they assume about the
// runner that produced the failure.
type RetryStrategy uint8

const (
	// RetryManager re-queues the task through the manager's generic retry.
	RetryManager RetryStrategy = iota
	// RetryRecover rebuilds the runner's input and recovers the task in place,
	// used by types whose interrupted work can be resumed from persisted items.
	RetryRecover
	// RetryWakeRunner wakes the runner that is already waiting out a backoff,
	// so no second runner is started for the same task.
	RetryWakeRunner
)

// CreationPath names the creation path that builds a type's tasks. The
// implementations live in pkg/core (this package stays free of runtime
// behavior); declaring the path here keeps "which types the API can create"
// part of the type's declaration.
type CreationPath uint8

const (
	CreationUpload CreationPath = iota
	CreationUploadStream
	CreationUploadStreamDirect
	CreationDownload
	CreationDownloadStream
	CreationDelete
	CreationCopy
	CreationMove
)

// Descriptor declares one task type's policy in a single place: how requests of
// that type map to an operation, whether a multi-item request is represented by
// a batch type, the creation defaults for durability and app visibility, and
// how interrupted or failed work is recovered and retried.
//
// Every declared Type has exactly one descriptor; descriptor_test.go enforces
// that, and creation rejects a type without one.
type Descriptor struct {
	Type Type
	// Creation is the creation path that builds tasks of this type.
	Creation CreationPath
	// Operation is the stable operation category a task of this type performs.
	// It is persisted with the task and feeds `durableKey`.
	Operation OperationKind
	// BatchOf is the type a multi-item request of this family is promoted to.
	// Empty means the family has no batch type: multi-item requests keep Type.
	BatchOf Type
	// Persistent and Dismissible are creation defaults only. The task's own
	// capability flags stay authoritative: replay normalization and runtime
	// upgrades (a move that crosses mounts, a recursive download) may differ.
	Persistent  bool
	Dismissible bool
	// UserVisible marks the types that the task API creates for the app's
	// default task list; ScopeForType turns it into the task's Scope. Internal
	// bookkeeping records that describe the same operation (the mount's own
	// upload/delete records) are created as sync scope explicitly.
	UserVisible bool
	// Recoverable marks types whose interrupted work is recovered at startup.
	Recoverable bool
	// Retry is the strategy used when the caller retries a failed task.
	Retry RetryStrategy
}

// descriptors is the single declaration of per-type policy. Adding a task type
// means adding its constant and one row here.
var descriptors = []Descriptor{
	{Type: TypeUploadRemote, Creation: CreationUpload, Operation: OperationUpload, BatchOf: TypeUploadBatch, Persistent: true, Dismissible: true, UserVisible: true},
	{Type: TypeUploadBatch, Creation: CreationUpload, Operation: OperationUpload, Persistent: true, Dismissible: true, UserVisible: true},
	{Type: TypeUploadStreamBatch, Creation: CreationUploadStream, Operation: OperationUpload, Persistent: true, Dismissible: true, UserVisible: true, Recoverable: true, Retry: RetryRecover},
	{Type: TypeUploadStreamDirect, Creation: CreationUploadStreamDirect, Operation: OperationUpload, Persistent: true, Dismissible: true, UserVisible: true, Recoverable: true, Retry: RetryWakeRunner},
	{Type: TypeDownload, Creation: CreationDownload, Operation: OperationDownload, UserVisible: true},
	{Type: TypeDownloadStreamBatch, Creation: CreationDownloadStream, Operation: OperationDownload, Persistent: true, Dismissible: true, UserVisible: true},
	{Type: TypeDeleteRemote, Creation: CreationDelete, Operation: OperationDelete, BatchOf: TypeDeleteBatch, UserVisible: true},
	{Type: TypeDeleteBatch, Creation: CreationDelete, Operation: OperationDelete, Persistent: true, Dismissible: true, UserVisible: true},
	{Type: TypeCopy, Creation: CreationCopy, Operation: OperationCopy, UserVisible: true},
	{Type: TypeMoveRemote, Creation: CreationMove, Operation: OperationMove, BatchOf: TypeMoveBatch, UserVisible: true},
	{Type: TypeMoveBatch, Creation: CreationMove, Operation: OperationMove, Persistent: true, Dismissible: true, UserVisible: true},
}

// CreationCapabilities returns the capability defaults a task of this type is
// created with. A task's own flags stay authoritative after creation: replay
// normalization and runtime upgrades may change them.
func (d Descriptor) CreationCapabilities() Capabilities {
	return Capabilities{Cancelable: true, Persistent: d.Persistent, Dismissible: d.Dismissible}
}

// Describe returns the descriptor for a task type, reporting whether the type
// is declared at all.
func Describe(typ Type) (Descriptor, bool) {
	for _, d := range descriptors {
		if d.Type == typ {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Descriptors returns a copy of every declared type descriptor.
func Descriptors() []Descriptor {
	return append([]Descriptor(nil), descriptors...)
}

// RecoverableTypes returns the types whose interrupted work is recovered at
// startup, in declaration order.
func RecoverableTypes() []Type {
	var types []Type
	for _, d := range descriptors {
		if d.Recoverable {
			types = append(types, d.Type)
		}
	}
	return types
}

// Promote returns the type a request of typ carrying items items should use: a
// family that declares a batch type promotes to it for multi-item requests,
// every other type (an explicitly requested batch type included) keeps its own.
func Promote(typ Type, items int) Type {
	d, ok := Describe(typ)
	if !ok || d.BatchOf == "" || items < 2 {
		return typ
	}
	return d.BatchOf
}

// ScopeForType returns the scope a task of typ is created with: app-visible
// types are user scope, bookkeeping types are sync scope. The app's default
// task list filters on this scope.
func ScopeForType(typ Type) Scope {
	if d, ok := Describe(typ); ok && d.UserVisible {
		return ScopeUser
	}
	return ScopeSync
}
