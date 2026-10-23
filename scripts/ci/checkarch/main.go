// Command checkarch enforces qrypt's package dependency policy and the
// upload-domain ownership seam. It inspects Go metadata and syntax trees, not
// source text, so comments, formatting and local variable names are irrelevant.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type listedPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

var packagePolicy = map[string]map[string]bool{
	"buildinfo":    {},
	"config":       allow("crypt", "drive", "util"),
	"contracttest": allow("drive", "vfs"),
	"control":      allow("buildinfo", "contracttest", "drive", "logging", "task", "util", "vfs"),
	"core":         allow("config", "control", "crypt", "drive", "limits", "logging", "media", "task", "util", "vfs"),
	"crypt":        allow("drive"),
	"drive":        allow("util"),
	"drivers":      allow("drive", "logging", "util"),
	"install":      allow("util"),
	"limits":       {},
	"logging":      allow("util"),
	"media":        allow("limits"),
	"mobile":       allow("core", "drive", "drivers", "logging"),
	"mount":        allow("drive", "logging", "vfs"),
	"selfupdate":   {},
	"syncer":       allow("drive", "util", "vfs"),
	"task":         allow("util"),
	"util":         {},
	"vfs":          allow("drive", "logging", "task", "util"),
}

func allow(names ...string) map[string]bool {
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = true
	}
	return result
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	module, err := commandOutput("go", "list", "-m", "-f", "{{.Path}}")
	if err != nil {
		fatal(err)
	}
	packages, err := listPackages()
	if err != nil {
		fatal(err)
	}
	var violations []string
	violations = append(violations, validateImports(strings.TrimSpace(module), packages)...)
	ownership, err := validateUploadOwnership(root, strings.TrimSpace(module))
	if err != nil {
		fatal(err)
	}
	violations = append(violations, ownership...)
	if len(violations) != 0 {
		sort.Strings(violations)
		for _, violation := range violations {
			fmt.Printf("!! %s\n", violation)
		}
		fmt.Println("== FAIL: architecture boundary violated ==")
		os.Exit(1)
	}
	fmt.Println("== architecture boundaries clean ==")
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "checkarch: %v\n", err)
	os.Exit(2)
}

func commandOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func listPackages() ([]listedPackage, error) {
	command := exec.Command("go", "list", "-json", "./pkg/...")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(stdout)
	var packages []listedPackage
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		packages = append(packages, pkg)
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("go list -json ./pkg/...: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return packages, nil
}

func validateImports(module string, packages []listedPackage) []string {
	var violations []string
	for _, pkg := range packages {
		top, ok := topLevelPackage(module, pkg.ImportPath)
		if !ok {
			continue
		}
		policy, registered := packagePolicy[top]
		if !registered {
			violations = append(violations, fmt.Sprintf("%s has no dependency policy", pkg.ImportPath))
			continue
		}
		for _, imported := range pkg.Imports {
			if isApplicationInternal(module, imported) {
				violations = append(violations, fmt.Sprintf("%s imports private application package %s", pkg.ImportPath, imported))
				continue
			}
			dependency, local := topLevelPackage(module, imported)
			if !local || dependency == top {
				continue
			}
			if !policy[dependency] {
				violations = append(violations, fmt.Sprintf("%s imports disallowed package %s", pkg.ImportPath, imported))
				continue
			}
			if top == "mobile" && dependency == "drivers" && imported != module+"/pkg/drivers/all" {
				violations = append(violations, fmt.Sprintf("%s may import only pkg/drivers/all, not %s", pkg.ImportPath, imported))
			}
		}
		testImports := append(append([]string{}, pkg.TestImports...), pkg.XTestImports...)
		for _, imported := range testImports {
			if isApplicationInternal(module, imported) {
				violations = append(violations, fmt.Sprintf("tests for %s import private application package %s", pkg.ImportPath, imported))
			}
			if top == "drivers" && isUpperLayer(module, imported) {
				violations = append(violations, fmt.Sprintf("driver tests for %s import upper layer %s", pkg.ImportPath, imported))
			}
		}
	}
	return violations
}

func topLevelPackage(module, importPath string) (string, bool) {
	prefix := module + "/pkg/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(importPath, prefix)
	if rest == "" {
		return "", false
	}
	return strings.Split(rest, "/")[0], true
}

func isApplicationInternal(module, importPath string) bool {
	return importPath == module+"/internal" || strings.HasPrefix(importPath, module+"/internal/")
}

func isUpperLayer(module, importPath string) bool {
	for _, prefix := range []string{
		module + "/pkg/vfs",
		module + "/pkg/mount",
		module + "/pkg/control",
		module + "/internal/cli",
	} {
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	return false
}

var forbiddenUploadTypes = map[string]bool{
	"PendingStore": true,
	"HashTracker":  true,
	"Engine":       true,
	"Service":      true,
}

func validateUploadOwnership(root, module string) ([]string, error) {
	rootFiles, err := parseGoDir(filepath.Join(root, "pkg", "vfs"))
	if err != nil {
		return nil, err
	}
	uploadFiles, err := parseGoDir(filepath.Join(root, "pkg", "vfs", "upload"))
	if err != nil {
		return nil, err
	}
	var violations []string
	violations = append(violations, validateVFSOwner(rootFiles, module+"/pkg/vfs/upload")...)
	violations = append(violations, validateDomainInterface(uploadFiles)...)
	return violations, nil
}

type parsedFile struct {
	fset    *token.FileSet
	file    *ast.File
	imports map[string]string
}

func parseGoDir(dir string) ([]parsedFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []parsedFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		imports := map[string]string{}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, err
			}
			name := filepath.Base(importPath)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imports[name] = importPath
		}
		files = append(files, parsedFile{fset: fset, file: file, imports: imports})
	}
	return files, nil
}

func validateVFSOwner(files []parsedFile, uploadImport string) []string {
	var violations []string
	tainted := taintedRootTypes(files, uploadImport)
	for _, parsed := range files {
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, item := range general.Specs {
				typeSpec := item.(*ast.TypeSpec)
				if typeSpec.Name.Name != "VFS" {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					if selectorUsesForbiddenUploadType(field.Type, parsed.imports, uploadImport) || expressionUsesNames(field.Type, tainted) {
						position := parsed.fset.Position(field.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: VFS must own upload state only through upload.Domain", position.Filename, position.Line))
					}
				}
			}
		}
	}
	return violations
}

func taintedRootTypes(files []parsedFile, uploadImport string) map[string]bool {
	tainted := map[string]bool{}
	types := map[string]struct {
		expr    ast.Expr
		imports map[string]string
	}{}
	for _, parsed := range files {
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, item := range general.Specs {
				typeSpec := item.(*ast.TypeSpec)
				types[typeSpec.Name.Name] = struct {
					expr    ast.Expr
					imports map[string]string
				}{typeSpec.Type, parsed.imports}
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for name, definition := range types {
			if tainted[name] {
				continue
			}
			if selectorUsesForbiddenUploadType(definition.expr, definition.imports, uploadImport) || expressionUsesNames(definition.expr, tainted) {
				tainted[name] = true
				changed = true
			}
		}
	}
	return tainted
}

func selectorUsesForbiddenUploadType(expr ast.Expr, imports map[string]string, uploadImport string) bool {
	forbidden := false
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if ok && imports[qualifier.Name] == uploadImport && forbiddenUploadTypes[selector.Sel.Name] {
			forbidden = true
			return false
		}
		return true
	})
	return forbidden
}

func validateDomainInterface(files []parsedFile) []string {
	var violations []string
	forbiddenNames := taintedUploadTypes(files)
	for _, parsed := range files {
		for _, declaration := range parsed.file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				for _, item := range declaration.Specs {
					typeSpec, ok := item.(*ast.TypeSpec)
					if !ok || typeSpec.Name.Name != "Domain" {
						continue
					}
					structure, ok := typeSpec.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structure.Fields.List {
						if len(field.Names) > 0 && field.Names[0].IsExported() && expressionUsesNames(field.Type, forbiddenNames) {
							position := parsed.fset.Position(field.Pos())
							violations = append(violations, fmt.Sprintf("%s:%d: upload.Domain exposes an implementation object", position.Filename, position.Line))
						}
					}
				}
			case *ast.FuncDecl:
				if declaration.Recv == nil || !declaration.Name.IsExported() || !receiverNamed(declaration.Recv, "Domain") || declaration.Type.Results == nil {
					continue
				}
				for _, result := range declaration.Type.Results.List {
					if expressionUsesNames(result.Type, forbiddenNames) {
						position := parsed.fset.Position(result.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: upload.Domain.%s returns an implementation object", position.Filename, position.Line, declaration.Name.Name))
					}
				}
			}
		}
	}
	return violations
}

func taintedUploadTypes(files []parsedFile) map[string]bool {
	tainted := make(map[string]bool, len(forbiddenUploadTypes))
	for name := range forbiddenUploadTypes {
		tainted[name] = true
	}
	types := map[string]ast.Expr{}
	for _, parsed := range files {
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, item := range general.Specs {
				typeSpec := item.(*ast.TypeSpec)
				if typeSpec.Name.Name != "Domain" {
					types[typeSpec.Name.Name] = typeSpec.Type
				}
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for name, expr := range types {
			if !tainted[name] && expressionUsesNames(expr, tainted) {
				tainted[name] = true
				changed = true
			}
		}
	}
	return tainted
}

func receiverNamed(fields *ast.FieldList, name string) bool {
	if fields == nil || len(fields.List) != 1 {
		return false
	}
	expr := fields.List[0].Type
	if pointer, ok := expr.(*ast.StarExpr); ok {
		expr = pointer.X
	}
	identifier, ok := expr.(*ast.Ident)
	return ok && identifier.Name == name
}

func expressionUsesNames(expr ast.Expr, names map[string]bool) bool {
	forbidden := false
	ast.Inspect(expr, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && names[identifier.Name] {
			forbidden = true
			return false
		}
		return true
	})
	return forbidden
}
