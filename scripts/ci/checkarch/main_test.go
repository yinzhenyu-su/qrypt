package main

import (
	"go/parser"
	"go/token"
	"testing"
)

const testModule = "example.com/qrypt"

func TestValidateImportsUsesPackageMetadata(t *testing.T) {
	t.Parallel()
	packages := []listedPackage{
		{
			ImportPath: testModule + "/pkg/mobile",
			Imports:    []string{testModule + "/pkg/core", testModule + "/pkg/drivers/localfs"},
		},
		{
			ImportPath:  testModule + "/pkg/drivers/example",
			Imports:     []string{testModule + "/pkg/drive"},
			TestImports: []string{testModule + "/pkg/vfs"},
		},
	}

	violations := validateImports(testModule, packages)
	if len(violations) != 2 {
		t.Fatalf("violations = %v, want mobile concrete-driver and driver-test violations", violations)
	}
}

func TestValidateImportsAllowsDeclaredDependencies(t *testing.T) {
	t.Parallel()
	packages := []listedPackage{{
		ImportPath: testModule + "/pkg/mobile",
		Imports: []string{
			testModule + "/pkg/core",
			testModule + "/pkg/drive",
			testModule + "/pkg/drivers/all",
		},
	}}

	if violations := validateImports(testModule, packages); len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
}

func TestValidateDomainInterfaceRejectsImplementationReturn(t *testing.T) {
	t.Parallel()
	parsed := parseSource(t, `package upload
type PendingStore struct{}
type Domain struct{}
func (d *Domain) Store() *PendingStore { return nil }
`)

	violations := validateDomainInterface([]parsedFile{parsed})
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want one implementation leak", violations)
	}
}

func TestValidateDomainInterfaceRejectsAliasedImplementationReturn(t *testing.T) {
	t.Parallel()
	parsed := parseSource(t, `package upload
type PendingStore struct{}
type StoreView = PendingStore
type Domain struct{}
func (d *Domain) Store() *StoreView { return nil }
`)

	violations := validateDomainInterface([]parsedFile{parsed})
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want one aliased implementation leak", violations)
	}
}

func TestValidateVFSOwnerResolvesImportAlias(t *testing.T) {
	t.Parallel()
	parsed := parseSource(t, `package vfs
import up "example.com/qrypt/pkg/vfs/upload"
type VFS struct { store *up.PendingStore }
`)

	violations := validateVFSOwner([]parsedFile{parsed}, testModule+"/pkg/vfs/upload")
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want one direct ownership violation", violations)
	}
}

func parseSource(t *testing.T, source string) parsedFile {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]string{}
	for _, spec := range file.Imports {
		name := spec.Name.Name
		imports[name] = testModule + "/pkg/vfs/upload"
	}
	return parsedFile{fset: fset, file: file, imports: imports}
}
