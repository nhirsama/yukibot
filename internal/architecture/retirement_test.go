package architecture

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoLegacyPythonRuntime(t *testing.T) {
	root := moduleRoot(t)
	for _, directory := range []string{"src", "tests", "scripts"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".py" {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if filepath.ToSlash(relative) != "scripts/import_sqlite.py" {
				t.Errorf("legacy Python runtime/test reintroduced: %s", relative)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"pyproject.toml", "uv.lock", "setup.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("Python runtime packaging reintroduced: %s", name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".github/workflows/ci.yml", "scripts/check-runtime.sh", "Dockerfile"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range []string{"setup-python", "setup-uv", "uv run", "pytest", "pip install"} {
			if strings.Contains(string(content), dependency) {
				t.Errorf("%s depends on retired tooling %q", name, dependency)
			}
		}
	}
}
