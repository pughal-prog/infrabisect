package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/report"
)

// setup builds a repository where commit `culprit` (1-based, after the
// baseline) writes "broken" into app/state.txt, plus an exec source of
// feature-flag changes interleaved in time.
func setup(t *testing.T, commits, culprit int, check string) *config.Config {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), append([]string{
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
		}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(nil, "init", "-q", "-b", "main")
	git(nil, "config", "commit.gpgsign", "false")
	base := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	state := "healthy"
	for i := 0; i <= commits; i++ {
		if i == culprit {
			state = "broken"
		}
		// One new file per commit plus a state file that only the culprit
		// edits, so reverting the culprit applies cleanly on top of HEAD.
		if err := os.WriteFile(filepath.Join(repo, "app", fmt.Sprintf("rev-%d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "app", "state.txt"), []byte("state: "+state+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		date := base.Add(time.Duration(i) * 10 * time.Minute).Format(time.RFC3339)
		git(nil, "add", "-A")
		git([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", fmt.Sprintf("change %d", i))
		if i == 0 {
			git(nil, "tag", "known-good")
		}
	}

	// Flags flip at minutes 5, 25, 45, ... - between the commits.
	var entries []string
	for i := 0; i < 3; i++ {
		entries = append(entries, fmt.Sprintf(`{"version":"f%d","time":"%s","summary":"flag change %d"}`,
			i, base.Add(time.Duration(5+20*i)*time.Minute).Format(time.RFC3339), i))
	}
	if err := os.WriteFile(filepath.Join(dir, "flags.json"), []byte("["+strings.Join(entries, ",")+"]"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw := fmt.Sprintf(`version: 1
workDir: work
sources:
  - name: app
    type: git
    repo: repo
    path: app
    good: known-good
  - name: flags
    type: exec
    list: cat flags.json
    good: f0
    render:
      command: echo "$INFRABISECT_VERSION" > "$INFRABISECT_OUT_DIR/flag.txt"
environment:
  type: dry
checks:
  - name: state
    type: command
    run: %q
`, check)
	loaded, err := config.Parse([]byte(raw), dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func requireShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX sh not available")
	}
}

const brokenCheck = `if grep -q broken "$INFRABISECT_STATE_DIR/app/state.txt"; then echo "app is broken"; exit 1; fi`

func TestRunFindsCulpritAcrossSources(t *testing.T) {
	requireShell(t)
	for _, culprit := range []int{1, 4, 9} {
		t.Run(fmt.Sprint(culprit), func(t *testing.T) {
			cfg := setup(t, 9, culprit, brokenCheck)
			var progress bytes.Buffer
			r, err := New(cfg, &progress, false)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx := context.Background()
			w, _ := r.Window("", "")
			tl, err := r.Timeline(ctx, w)
			if err != nil {
				t.Fatal(err)
			}
			if tl.Len() != 11 { // 9 commits + 2 flag changes after f0
				t.Fatalf("timeline has %d changes, want 11", tl.Len())
			}
			rep, err := r.Run(ctx, tl, w, Options{VerifyBad: true, Revert: true, Version: "test"})
			if err != nil {
				t.Fatalf("%v\n%s", err, progress.String())
			}
			if rep.Status != report.StatusCulprit {
				t.Fatalf("status %s: %s\n%s", rep.Status, rep.Message, progress.String())
			}
			if rep.Culprit.Source != "app" || rep.Culprit.Summary != fmt.Sprintf("change %d", culprit) {
				t.Fatalf("culprit = %+v", rep.Culprit)
			}
			if rep.Revert == "" {
				t.Fatalf("revert not prepared: %s", rep.RevertError)
			}
			var md bytes.Buffer
			rep.WriteMarkdown(&md)
			if !strings.Contains(md.String(), "## Culprit") || !strings.Contains(md.String(), "git -C") {
				t.Fatalf("markdown report incomplete:\n%s", md.String())
			}
			if _, err := os.Stat(filepath.Join(r.WorkDir, "states")); !os.IsNotExist(err) {
				t.Fatal("states directory was not cleaned up")
			}
		})
	}
}

func TestRunNotReproducible(t *testing.T) {
	requireShell(t)
	cfg := setup(t, 4, 99, "exit 0")
	r, err := New(cfg, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	w, _ := r.Window("", "")
	tl, _ := r.Timeline(ctx, w)
	rep, err := r.Run(ctx, tl, w, Options{VerifyBad: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != report.StatusNotReproducible || rep.ExitCode() != 2 {
		t.Fatalf("status = %s", rep.Status)
	}
}

func TestRunSkipsUntestableStates(t *testing.T) {
	requireShell(t)
	// Revision 5 cannot be tested (exit 125); the culprit is revision 6, so
	// the search must route around 5 and still narrow to 6... or report
	// both as candidates when 5 is the only boundary witness.
	check := `d="$INFRABISECT_STATE_DIR/app"; [ -f "$d/rev-5.txt" ] && [ ! -f "$d/rev-6.txt" ] && exit 125; ` + brokenCheck
	cfg := setup(t, 9, 6, check)
	r, err := New(cfg, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	w, _ := r.Window("", "")
	tl, _ := r.Timeline(ctx, w)
	rep, err := r.Run(ctx, tl, w, Options{VerifyBad: true})
	if err != nil {
		t.Fatal(err)
	}
	switch rep.Status {
	case report.StatusCulprit:
		if rep.Culprit.Summary != "change 6" {
			t.Fatalf("culprit = %+v", rep.Culprit)
		}
	case report.StatusInconclusive:
		found := false
		for _, c := range rep.Candidates {
			found = found || c.Summary == "change 6"
		}
		if !found {
			t.Fatalf("candidates do not include change 6: %+v", rep.Candidates)
		}
	default:
		t.Fatalf("status = %s: %s", rep.Status, rep.Message)
	}
}
