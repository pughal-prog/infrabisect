package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pughal-prog/infrabisect/internal/config"
)

func run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, IO{Stdout: &out, Stderr: &errOut}, BuildInfo{Version: "1.2.3", Commit: "abc"})
	return code, out.String(), errOut.String()
}

func TestEveryTemplateIsValid(t *testing.T) {
	for _, name := range templateNames() {
		if _, err := config.Parse([]byte(templates[name]), t.TempDir()); err != nil {
			t.Errorf("template %s: %v", name, err)
		}
	}
}

func TestInitThenValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infrabisect.yaml")
	if code, _, stderr := run("init", "-template", "dry", "-o", path); code != 0 {
		t.Fatalf("init failed: %s", stderr)
	}
	if code, _, _ := run("init", "-o", path); code == 0 {
		t.Fatal("init overwrote an existing file without -force")
	}
	code, stdout, stderr := run("validate", "-c", path)
	if code != 0 || !strings.Contains(stdout, "is valid") {
		t.Fatalf("validate: %d %s %s", code, stdout, stderr)
	}
}

func TestVersionAndUnknown(t *testing.T) {
	if _, out, _ := run("version"); !strings.Contains(out, "1.2.3") {
		t.Fatalf("version output %q", out)
	}
	if code, _, _ := run("frobnicate"); code != 1 {
		t.Fatal("unknown command should exit 1")
	}
	if code, _, _ := run("run", "-h"); code != 0 {
		t.Fatal("-h should exit 0")
	}
}
