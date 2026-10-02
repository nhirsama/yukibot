package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Authorization belongs to one dispatcher boundary, not producer-, role- or
// feature-specific execution paths. Tests may exercise the policy directly.
func TestSingleCommandAuthorizationBoundary(t *testing.T) {
	root := moduleRoot(t)
	var callers []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "IsAuthorized" {
				relative, _ := filepath.Rel(root, path)
				callers = append(callers, filepath.ToSlash(relative))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0] != "internal/kernel/control.go" {
		t.Fatalf("authorization must run only in the command dispatcher: %v", callers)
	}
}

func TestImportBoundaries(t *testing.T) {
	root := moduleRoot(t)
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		imports, err := fileImports(path)
		if err != nil {
			return err
		}
		violations = append(violations, boundaryViolations(rel, imports)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("import boundaries:\n%s", strings.Join(violations, "\n"))
	}
}

func boundaryViolations(file string, imports []string) []string {
	var violations []string
	feature, inFeature := featureOf(file)
	for _, imported := range imports {
		internal, ok := internalPath(imported)
		switch {
		case isGotd(imported) && !gotdAllowed(file):
			violations = append(violations, file+": "+imported)
		case isPgx(imported) && !pgxAllowed(file):
			violations = append(violations, file+": "+imported)
		case !ok:
			continue
		}
		if !ok {
			continue
		}
		if (under(file, "internal/kernel") || under(file, "internal/contracts")) && importsOuter(internal) {
			violations = append(violations, file+": "+imported)
		}
		if under(file, "internal/adapters") && (under(internal, "internal/features") || under(internal, "internal/kernel")) {
			violations = append(violations, file+": "+imported)
		}
		if inFeature && under(internal, "internal/features") {
			other, _ := featureOf(internal)
			if other != "" && other != feature {
				violations = append(violations, file+": "+imported)
			}
		}
		if inFeature && feature != "management" && under(internal, "internal/kernel") {
			violations = append(violations, file+": "+imported)
		}
		if inFeature && (under(internal, "internal/bootstrap") || under(internal, "internal/config")) {
			violations = append(violations, file+": "+imported)
		}
		if inFeature && under(internal, "internal/adapters") && !(infraFile(file) && under(internal, "internal/adapters/telegram")) {
			violations = append(violations, file+": "+imported)
		}
	}
	return violations
}

func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func featureOf(path string) (string, bool) {
	const prefix = "internal/features/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	name, _, _ := strings.Cut(rest, "/")
	return name, name != ""
}

func infraFile(path string) bool {
	feature, ok := featureOf(path)
	return ok && strings.HasPrefix(path, "internal/features/"+feature+"/infra/")
}

func gotdAllowed(path string) bool {
	return under(path, "internal/adapters/telegram") || infraFile(path)
}

func pgxAllowed(path string) bool {
	if under(path, "internal/adapters/database") || under(path, "internal/storage/dbsql") {
		return true
	}
	feature, ok := featureOf(path)
	return ok && strings.HasPrefix(path, "internal/features/"+feature+"/store/")
}

func importsOuter(path string) bool {
	return under(path, "internal/features") ||
		under(path, "internal/adapters") ||
		under(path, "internal/bootstrap") ||
		under(path, "internal/config")
}

func internalPath(imported string) (string, bool) {
	const module = "github.com/nhirsama/yukibot/"
	if !strings.HasPrefix(imported, module) {
		return "", false
	}
	return strings.TrimPrefix(imported, module), true
}

func isGotd(imported string) bool { return strings.HasPrefix(imported, "github.com/gotd/td") }

func isPgx(imported string) bool { return strings.HasPrefix(imported, "github.com/jackc/pgx") }

func fileImports(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, `"`))
	}
	return imports, nil
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
