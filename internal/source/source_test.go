package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/model"
)

func ts(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }

const helmHistory = `[
 {"revision":3,"updated":"2026-10-27T10:00:00.123+00:00","status":"superseded","chart":"payments-1.3.0","app_version":"2.1","description":"Upgrade complete"},
 {"revision":1,"updated":"2026-10-26T08:00:00Z","status":"superseded","chart":"payments-1.1.0","app_version":"2.0","description":"Install complete"},
 {"revision":2,"updated":"2026-10-27T09:00:00Z","status":"superseded","chart":"payments-1.2.0","app_version":"2.0","description":"Upgrade complete"},
 {"revision":4,"updated":"2026-10-27T11:00:00Z","status":"deployed","chart":"payments-1.4.0","app_version":"2.2","description":"Upgrade complete"}
]`

func TestHelmStreamWindow(t *testing.T) {
	cfg := config.Source{Name: "payments", Release: "payments"}
	s, err := buildHelmStream(cfg, []byte(helmHistory), Window{Since: ts(8, 30)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Baseline != "1" || s.Head != "4" || len(s.Changes) != 3 {
		t.Fatalf("stream = %+v", s)
	}
	if c := s.Changes[0]; c.Version != "2" || c.Prev != "1" || !strings.Contains(c.Summary, "payments-1.2.0") {
		t.Fatalf("first change = %+v", c)
	}

	s, err = buildHelmStream(cfg, []byte(helmHistory), Window{Since: ts(8, 30), Until: ts(10, 30)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Head != "3" || len(s.Changes) != 2 {
		t.Fatalf("until not applied: %+v", s)
	}

	cfg.Good, cfg.Bad = "2", "3"
	s, err = buildHelmStream(cfg, []byte(helmHistory), Window{})
	if err != nil || len(s.Changes) != 1 || s.Changes[0].Version != "3" {
		t.Fatalf("explicit range: %+v %v", s, err)
	}
	r := newHelm(config.Source{Name: "payments", Release: "payments", Namespace: "prod"}, Runtime{}).Remedy(s.Changes[0])
	if r.Commands[0] != "helm rollback payments 2 --namespace prod" {
		t.Fatalf("remedy = %v", r.Commands)
	}
}

func TestHelmStreamNeedsBaseline(t *testing.T) {
	if _, err := buildHelmStream(config.Source{Name: "p", Release: "p"}, []byte(helmHistory), Window{}); err == nil {
		t.Fatal("expected error without good or since")
	}
}

func TestExecStream(t *testing.T) {
	list := `[
	 {"version":"v0","time":"2026-10-27T08:00:00Z","summary":"baseline"},
	 {"version":"v1","time":"2026-10-27T09:00:00Z","author":"ana","summary":"enable search"},
	 {"version":"v2","time":"2026-10-27T10:00:00Z","summary":"checkout 50%"},
	 {"version":"v3","time":"2026-10-27T11:00:00Z","summary":"checkout 100%"}
	]`
	cfg := config.Source{Name: "flags"}
	s, err := buildExecStream(cfg, []byte(list), Window{Since: ts(8, 30), Until: ts(10, 30)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Baseline != "v0" || len(s.Changes) != 2 || s.Changes[1].Prev != "v1" || s.Head != "v2" {
		t.Fatalf("stream = %+v", s)
	}

	cfg.Good = "v1"
	s, err = buildExecStream(cfg, []byte(list), Window{})
	if err != nil || len(s.Changes) != 2 || s.Changes[0].Version != "v2" {
		t.Fatalf("good=v1: %+v %v", s, err)
	}

	s, err = buildExecStream(config.Source{Name: "f"}, []byte(`{"baseline":"b","changes":[{"version":"x","time":"2026-10-27T09:00:00Z"}]}`), Window{})
	if err != nil || s.Baseline != "b" || len(s.Changes) != 1 {
		t.Fatalf("object form: %+v %v", s, err)
	}

	r := newExec(config.Source{Name: "flags", Remedy: "flagctl restore {{source}} {{prev}}"}, Runtime{}).Remedy(model.Change{Source: "flags", Version: "v2", Prev: "v1"})
	if r.Commands[0] != "flagctl restore flags v1" {
		t.Fatalf("remedy = %v", r.Commands)
	}
}

// gitRepo creates a repository with one commit per entry; files maps a
// path to its content in that commit.
func gitRepo(t *testing.T, commits []map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append([]string{
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com",
		}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(nil, "init", "-q", "-b", "main")
	run(nil, "config", "commit.gpgsign", "false")
	for i, files := range commits {
		for p, content := range files {
			full := filepath.Join(dir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		date := ts(9, i*10).Format(time.RFC3339)
		run(nil, "add", "-A")
		run([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", "commit "+string(rune('a'+i)))
		if i == 0 {
			run(nil, "tag", "good")
		}
	}
	return dir
}

func TestGitStreamAndRender(t *testing.T) {
	repo := gitRepo(t, []map[string]string{
		{"k8s/app.yaml": "v0", "README.md": "x"},
		{"k8s/app.yaml": "v1"},
		{"README.md": "docs only"}, // outside path: not a change
		{"k8s/app.yaml": "v2", "k8s/extra.yaml": "e"},
	})
	fp := true
	cfg := config.Source{Name: "app", Type: "git", Repo: repo, Path: "k8s", Good: "good", FirstParent: &fp, Render: config.Render{Type: "raw"}}
	g := newGit(cfg, Runtime{BaseDir: repo, CacheDir: t.TempDir()})
	ctx := context.Background()

	s, err := g.Stream(ctx, Window{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Changes) != 2 {
		t.Fatalf("changes = %+v", s.Changes)
	}
	if s.Changes[0].Summary != "commit b" || s.Changes[1].Summary != "commit d" || s.Changes[1].Prev != s.Changes[0].Version {
		t.Fatalf("changes = %+v", s.Changes)
	}
	if !s.Changes[0].Time.Equal(ts(9, 10)) {
		t.Fatalf("time = %v", s.Changes[0].Time)
	}

	out := t.TempDir()
	if err := g.Render(ctx, s.Baseline, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "app.yaml")); string(b) != "v0" {
		t.Fatalf("baseline render = %q", b)
	}
	out2 := t.TempDir()
	if err := g.Render(ctx, s.Changes[1].Version, out2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out2, "extra.yaml")); err != nil {
		t.Fatalf("head render missing extra.yaml: %v", err)
	}

	// Window: baseline is the last commit before since.
	cfg.Good = ""
	g = newGit(cfg, Runtime{BaseDir: repo, CacheDir: t.TempDir()})
	s, err = g.Stream(ctx, Window{Since: ts(9, 15)})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Changes) != 1 || s.Changes[0].Summary != "commit d" {
		t.Fatalf("window changes = %+v", s.Changes)
	}

	// Revert prepares a branch in a separate worktree, even on a machine
	// with no git identity configured (fresh CI runners).
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	desc, err := g.Revert(ctx, s.Changes[0], s.Head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "infrabisect/revert-") {
		t.Fatalf("revert = %s", desc)
	}
}
