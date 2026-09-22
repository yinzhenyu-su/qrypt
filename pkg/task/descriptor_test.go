package task

import "testing"

// declaredTypes is the golden list of task types: it exists so that a type
// constant added without a descriptor row (or vice versa) fails here.
var declaredTypes = []Type{
	TypeUploadRemote,
	TypeUploadBatch,
	TypeUploadStreamBatch,
	TypeUploadStreamDirect,
	TypeDownload,
	TypeDownloadStreamBatch,
	TypeDeleteRemote,
	TypeDeleteBatch,
	TypeCopy,
	TypeMoveRemote,
	TypeMoveBatch,
}

func TestDescriptorsCoverEveryDeclaredType(t *testing.T) {
	seen := map[Type]Descriptor{}
	for _, d := range Descriptors() {
		if d.Type == "" {
			t.Fatal("descriptor without a type")
		}
		if _, dup := seen[d.Type]; dup {
			t.Fatalf("duplicate descriptor for type %q", d.Type)
		}
		seen[d.Type] = d
	}
	for _, typ := range declaredTypes {
		if _, ok := seen[typ]; !ok {
			t.Errorf("declared task type %q has no descriptor", typ)
		}
	}
	if len(seen) != len(declaredTypes) {
		t.Errorf("descriptor count = %d, want %d (one per declared type)", len(seen), len(declaredTypes))
	}
}

func TestDescriptorOperationIdentity(t *testing.T) {
	for _, d := range Descriptors() {
		if d.Operation == "" {
			t.Errorf("type %q declares no operation", d.Type)
			continue
		}
		if !validOperationKind(d.Operation) {
			t.Errorf("type %q declares unknown operation %q", d.Type, d.Operation)
		}
		if got := OperationForType(d.Type); got != d.Operation {
			t.Errorf("OperationForType(%q) = %q, want the declared %q", d.Type, got, d.Operation)
		}
	}
	if got := OperationForType(Type("not_a_type")); got != "" {
		t.Fatalf("undeclared type maps to operation %q, want empty", got)
	}
}

// TestDescriptorBatchTargetsAreFamilyHeads: a promoted type must exist, belong
// to the same operation, and must not itself promote, so promotion is a single
// step and never chains.
func TestDescriptorBatchTargetsAreFamilyHeads(t *testing.T) {
	for _, d := range Descriptors() {
		if d.BatchOf == "" {
			if got := Promote(d.Type, 5); got != d.Type {
				t.Errorf("Promote(%q, 5) = %q, want the type itself (no batch type declared)", d.Type, got)
			}
			continue
		}
		target, ok := Describe(d.BatchOf)
		if !ok {
			t.Errorf("type %q promotes to undeclared type %q", d.Type, d.BatchOf)
			continue
		}
		if target.Operation != d.Operation {
			t.Errorf("type %q promotes across operations: %q is %q, %q is %q", d.Type, d.Type, d.Operation, target.Type, target.Operation)
		}
		if target.BatchOf != "" {
			t.Errorf("type %q promotes to %q which promotes again to %q", d.Type, target.Type, target.BatchOf)
		}
		if got := Promote(d.Type, 2); got != d.BatchOf {
			t.Errorf("Promote(%q, 2) = %q, want %q", d.Type, got, d.BatchOf)
		}
		if got := Promote(d.Type, 1); got != d.Type {
			t.Errorf("Promote(%q, 1) = %q, want the type itself", d.Type, got)
		}
		if got := Promote(target.Type, 5); got != target.Type {
			t.Errorf("an explicit batch type must keep its identity: Promote(%q, 5) = %q", target.Type, got)
		}
	}
}

// Sync origin is reserved for the syncer (glossary: Sync 任务来源 = syncer
// 作业产生). Nothing derives it: not declared types, not undeclared ones.
func TestScopeForTypeNeverDerivesSyncScope(t *testing.T) {
	t.Parallel()
	for _, d := range Descriptors() {
		if got := ScopeForType(d.Type); got == ScopeSync {
			t.Errorf("ScopeForType(%q) = %q, sync origin is syncer-only", d.Type, got)
		}
	}
	if got := ScopeForType(Type("not_a_type")); got == ScopeSync {
		t.Errorf("ScopeForType(undeclared) = %q, sync origin is syncer-only", got)
	}
}

// Scope and visibility are independent declarations (glossary: 任务来源 与
// 可见性 正交): each lookup returns the declaration verbatim and never derives
// one axis from the other.
func TestDescriptorDeclaresScopeAndVisibilityIndependently(t *testing.T) {
	t.Parallel()
	for _, d := range Descriptors() {
		if got := ScopeForType(d.Type); got != d.Scope {
			t.Errorf("ScopeForType(%q) = %q, want declared %q", d.Type, got, d.Scope)
		}
		if got := VisibilityForType(d.Type); got != d.Visibility {
			t.Errorf("VisibilityForType(%q) = %q, want declared %q", d.Type, got, d.Visibility)
		}
	}
	if got := ScopeForType(Type("not_a_type")); got != ScopeInternal {
		t.Fatalf("undeclared type scope = %q, want internal scope", got)
	}
	if got := VisibilityForType(Type("not_a_type")); got != VisibilityHidden {
		t.Fatalf("undeclared type visibility = %q, want hidden", got)
	}
	if len(RecoverableTypes()) == 0 {
		t.Fatal("no recoverable types declared; interrupted streaming work would never be recovered")
	}
}
