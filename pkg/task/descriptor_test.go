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

func TestDescriptorScopeFollowsVisibility(t *testing.T) {
	for _, d := range Descriptors() {
		want := ScopeSync
		if d.UserVisible {
			want = ScopeUser
		}
		if got := ScopeForType(d.Type); got != want {
			t.Errorf("ScopeForType(%q) = %q, want %q", d.Type, got, want)
		}
	}
	if got := ScopeForType(Type("not_a_type")); got != ScopeSync {
		t.Fatalf("undeclared type scope = %q, want sync scope", got)
	}
	if len(RecoverableTypes()) == 0 {
		t.Fatal("no recoverable types declared; interrupted streaming work would never be recovered")
	}
}
