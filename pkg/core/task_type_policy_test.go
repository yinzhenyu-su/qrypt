package core

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestCreationPathsMatchDeclaredTypes: every declared task type resolves to an
// implemented creation path, every implemented path is declared by some type,
// and an undeclared type is refused. A type therefore cannot be accepted
// without a creation path, or declared without one.
func TestCreationPathsMatchDeclaredTypes(t *testing.T) {
	declared := map[task.CreationPath]bool{}
	for _, descriptor := range task.Descriptors() {
		declared[descriptor.Creation] = true
		if _, ok := taskCreators[descriptor.Creation]; !ok {
			t.Errorf("type %q declares creation path %d with no implementation", descriptor.Type, descriptor.Creation)
		}
	}
	for path := range taskCreators {
		if !declared[path] {
			t.Errorf("creation path %d is implemented but no declared type uses it", path)
		}
	}

	core := &Core{}
	if _, err := core.CreateTask(context.Background(), task.Request{Type: task.Type("not_a_type")}); err == nil {
		t.Fatal("creation accepted an undeclared task type")
	}
}

// TestRecoveryPathsMatchDeclaredRecoverableTypes: the recovery implementations
// and the declared recoverable types are the same set, so interrupted work of a
// declared recoverable type always has a recovery path and no undeclared type
// is recovered.
func TestRecoveryPathsMatchDeclaredRecoverableTypes(t *testing.T) {
	declared := map[task.Type]bool{}
	for _, typ := range task.RecoverableTypes() {
		declared[typ] = true
		if _, ok := taskRecoveryPaths[typ]; !ok {
			t.Errorf("type %q is declared recoverable without a recovery implementation", typ)
		}
	}
	if len(declared) == 0 {
		t.Fatal("no recoverable types declared")
	}
	for typ := range taskRecoveryPaths {
		if !declared[typ] {
			t.Errorf("type %q has a recovery implementation but is not declared recoverable", typ)
		}
	}

	core := &Core{}
	if recovery := core.taskRecovery(task.Type("not_a_type")); recovery != nil {
		t.Fatal("undeclared type has a recovery implementation")
	}
}
