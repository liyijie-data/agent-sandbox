package architecture

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const ModulePrefix = "agent-platform"

var dbSDKPrefixes = []string{
	"gorm.io/",
	"github.com/jackc/pgx/",
	"database/sql",
}

var lowLevelRoots = []string{
	ModulePrefix + "/model",
	ModulePrefix + "/internal/lifecycle",
	ModulePrefix + "/internal/contracts",
}

var businessPkgs = []string{
	ModulePrefix + "/internal/runs",
	ModulePrefix + "/internal/executor",
	ModulePrefix + "/internal/controlplane",
	ModulePrefix + "/internal/events",
	ModulePrefix + "/internal/cleanup",
	ModulePrefix + "/internal/images",
	ModulePrefix + "/internal/modelgateway",
	ModulePrefix + "/internal/sandbox",
}

var sdkOwners = map[string][]string{
	"github.com/redis/go-redis": {
		ModulePrefix + "/internal/modelgateway",
		ModulePrefix + "/internal/storage",
		ModulePrefix + "/internal/queue",
	},
	"github.com/minio/minio-go": {
		ModulePrefix + "/internal/storage",
	},
	"k8s.io/": {
		ModulePrefix + "/internal/sandbox",

		ModulePrefix + "/internal/reconciler",
	},
	"sigs.k8s.io/": {
		ModulePrefix + "/internal/sandbox",
		ModulePrefix + "/internal/reconciler",
	},
}

var forbiddenHandleNames = map[string]bool{
	"ExecSQL": true,
	"WithTx":  true,
	"Pool":    true,
	"DB":      true,
}

type Violation struct {
	Package string
	Rule    string
	Detail  string
}

func (v Violation) String() string {
	return fmt.Sprintf("%-45s %s  %s", v.Package, v.Rule, v.Detail)
}

type Package struct {
	ImportPath  string
	Name        string
	MainImports []string
	TestImports []string
}

func (p *Package) imports() []string {
	return dedupStr(append(append([]string{}, p.MainImports...), p.TestImports...))
}

func goList(ctx context.Context, dir, tmpl string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-test", "-deps=false", "-e", "-f", tmpl, "./...")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(out.String()))
	}
	var res []string
	for _, l := range strings.Split(out.String(), "\n") {
		if tr := strings.TrimSpace(l); tr != "" {
			res = append(res, tr)
		}
	}
	return res, nil
}

func LoadPackages(ctx context.Context, moduleRoot string) (map[string]*Package, error) {
	lines, err := goList(ctx, moduleRoot,
		`{{.ImportPath}}|{{.Name}}|{{join .Imports ","}}|{{join .TestImports ","}}|{{join .XTestImports ","}}`)
	if err != nil {
		return nil, err
	}
	pkgs := make(map[string]*Package)
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) != 5 {
			continue
		}
		importPath, name := parts[0], parts[1]

		if strings.Contains(importPath, " [") || strings.HasSuffix(importPath, ".test") || name == "TestMain" {
			continue
		}
		if importPath != ModulePrefix && !strings.HasPrefix(importPath, ModulePrefix+"/") {
			continue
		}
		p := pkgs[importPath]
		if p == nil {
			p = &Package{ImportPath: importPath, Name: name}
			pkgs[importPath] = p
		}
		p.MainImports = dedupStr(append(p.MainImports, splitImports(parts[2])...))
		p.TestImports = dedupStr(append(p.TestImports, splitImports(parts[3])...))
		p.TestImports = dedupStr(append(p.TestImports, splitImports(parts[4])...))
	}
	return pkgs, nil
}

func FilterPackages(pkgs map[string]*Package, excludedPrefix string) map[string]*Package {
	out := make(map[string]*Package, len(pkgs))
	for path, p := range pkgs {
		if !strings.HasPrefix(path, excludedPrefix) {
			out[path] = p
		}
	}
	return out
}

func FindModuleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" || p == "/dev/null" {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(p), nil
}

func isDBSDKImport(imp string) bool {
	for _, pre := range dbSDKPrefixes {
		if strings.HasPrefix(imp, pre) {
			return true
		}
	}
	return false
}

func isModelRoot(pkgPath string) bool {
	return pkgPath == ModulePrefix+"/model" || strings.HasPrefix(pkgPath, ModulePrefix+"/model/")
}

func isLowLevel(pkgPath string) bool {
	for _, root := range lowLevelRoots {
		if pkgPath == root || strings.HasPrefix(pkgPath, root+"/") {
			return true
		}
	}
	return false
}

func isBusiness(imp string) bool {
	for _, b := range businessPkgs {
		if imp == b || strings.HasPrefix(imp, b+"/") {
			return true
		}
	}
	return false
}

func sdkOwnersFor(imp string) ([]string, bool) {
	for prefix, owners := range sdkOwners {
		if strings.HasPrefix(imp, prefix) {
			return owners, true
		}
	}
	return nil, false
}

func CheckPackage(p *Package) []Violation {
	var out []Violation

	if !isModelRoot(p.ImportPath) {
		for _, imp := range p.MainImports {
			if isDBSDKImport(imp) {
				out = append(out, Violation{Package: p.ImportPath, Rule: "R1", Detail: "database SDK import " + imp})
			}
		}
	}

	if !isModelRoot(p.ImportPath) {
		for _, imp := range p.TestImports {
			if isDBSDKImport(imp) {
				out = append(out, Violation{Package: p.ImportPath, Rule: "R5", Detail: "database SDK import in test " + imp})
			}
		}
	}

	if isLowLevel(p.ImportPath) {
		for _, up := range upwardImports(p.MainImports) {
			up.Package = p.ImportPath
			out = append(out, up)
		}
	}

	for _, imp := range p.imports() {
		owners, owned := sdkOwnersFor(imp)
		if !owned {
			continue
		}
		if !containsStr(owners, p.ImportPath) {
			out = append(out, Violation{
				Package: p.ImportPath,
				Rule:    "R4",
				Detail:  "SDK import " + imp + " owned by " + strings.Join(owners, ","),
			})
		}
	}

	return out
}

func upwardImports(imports []string) []Violation {
	var out []Violation
	for _, imp := range imports {
		if isBusiness(imp) {
			out = append(out, Violation{Rule: "R3", Detail: "low-level package must not import " + imp})
		}
	}
	return out
}

type ExportLeak struct {
	Package string
	Symbol  string
	Detail  string
}

func (l ExportLeak) String() string {
	return fmt.Sprintf("%s.%s exposes %s", l.Package, l.Symbol, l.Detail)
}

func FindExportLeaks(moduleRoot, pkgDir string) ([]ExportLeak, error) {
	fset := token.NewFileSet()
	entries, err := filepath.Glob(filepath.Join(pkgDir, "*.go"))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no Go files in %s", pkgDir)
	}
	var leaks []ExportLeak
	for _, file := range entries {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		astFile, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		imports := importBindings(astFile)
		for _, decl := range astFile.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				leaks = append(leaks, exportedFuncLeaks(moduleRoot, d, imports)...)
			case *ast.GenDecl:
				leaks = append(leaks, exportedGenLeaks(moduleRoot, d, imports)...)
			}
		}
	}
	return leaks, nil
}

func importBindings(f *ast.File) map[string]string {
	bindings := make(map[string]string)
	for _, imp := range f.Imports {
		path, err := unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		var name string
		if imp.Name != nil {
			name = imp.Name.Name
			if name == "_" || name == "." {
				continue
			}
		} else {
			name = lastPathElement(path)
		}

		if i := strings.LastIndex(name, "v"); i > 0 && name[:i] != "" && isMajorVersionSuffix(name[i:]) {
			name = name[:i-1]
		}
		bindings[name] = path
	}
	return bindings
}

func lastPathElement(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func isMajorVersionSuffix(s string) bool {
	if len(s) < 2 || s[0] != '/' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s[1:]) > 0
}

func exportedFuncLeaks(moduleRoot string, f *ast.FuncDecl, imports map[string]string) []ExportLeak {
	if !f.Name.IsExported() {
		return nil
	}
	var out []ExportLeak
	if forbiddenHandleNames[f.Name.Name] {
		out = append(out, ExportLeak{
			Package: moduleRoot,
			Symbol:  f.Name.Name,
			Detail:  "exports generic handle name (Pool/DB/ExecSQL/WithTx)",
		})
	}
	if f.Type.Results != nil {
		for _, res := range f.Type.Results.List {
			if pkg := referencedHandlePkg(res.Type, imports); pkg != "" {
				out = append(out, ExportLeak{
					Package: moduleRoot,
					Symbol:  f.Name.Name,
					Detail:  "return type leaks handle from " + pkg,
				})
			}
		}
	}
	return out
}

func exportedGenLeaks(moduleRoot string, d *ast.GenDecl, imports map[string]string) []ExportLeak {
	var out []ExportLeak
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			st, ok := s.Type.(*ast.StructType)
			if !ok || !s.Name.IsExported() {
				continue
			}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 || !field.Names[0].IsExported() {
					continue
				}
				if pkg := referencedHandlePkg(field.Type, imports); pkg != "" {
					out = append(out, ExportLeak{
						Package: moduleRoot,
						Symbol:  s.Name.Name + "." + field.Names[0].Name,
						Detail:  "field leaks handle from " + pkg,
					})
				}
			}
		case *ast.ValueSpec:
			if len(s.Names) > 0 && s.Names[0].IsExported() {
				if pkg := referencedHandlePkg(s.Type, imports); pkg != "" {
					out = append(out, ExportLeak{
						Package: moduleRoot,
						Symbol:  s.Names[0].Name,
						Detail:  "var leaks handle from " + pkg,
					})
				}
			}
		}
	}
	return out
}

func referencedHandlePkg(expr ast.Expr, imports map[string]string) string {
	if expr == nil {
		return ""
	}
	var found string
	ast.Inspect(expr, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path, ok := imports[x.Name]
		if !ok {
			return true
		}
		if isDBSDKImport(path) {
			found = path
		}
		return true
	})
	return found
}

func splitImports(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dedupStr(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func unquote(s string) (string, error) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	return s, fmt.Errorf("not a quoted string: %s", s)
}

func withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Minute)
}
