package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// execSource is the escape hatch for any system: a list command prints the
// change history as JSON and a render command materializes one version.
// Feature flags, Terraform Cloud state versions, image tags in a registry or
// a SaaS config API all fit this shape.
type execSource struct {
	cfg config.Source
	rt  Runtime
}

func newExec(cfg config.Source, rt Runtime) *execSource { return &execSource{cfg: cfg, rt: rt} }

func (e *execSource) Name() string { return e.cfg.Name }
func (e *execSource) Kind() string { return "exec" }

// execEntry is one element printed by the list command.
type execEntry struct {
	Version string `json:"version"`
	Time    string `json:"time"`
	Author  string `json:"author"`
	Summary string `json:"summary"`
}

func (e *execSource) Stream(ctx context.Context, w Window) (model.Stream, error) {
	env := []string{"INFRABISECT_SOURCE=" + e.cfg.Name}
	if !w.Since.IsZero() {
		env = append(env, "INFRABISECT_SINCE="+w.Since.UTC().Format(time.RFC3339))
	}
	if !w.Until.IsZero() {
		env = append(env, "INFRABISECT_UNTIL="+w.Until.UTC().Format(time.RFC3339))
	}
	res, err := execx.Shell(ctx, e.rt.Shell, execx.Cmd{Dir: e.rt.BaseDir, Env: env}, e.cfg.List)
	if err != nil {
		return model.Stream{Source: e.cfg.Name, Kind: "exec"}, fmt.Errorf("source %s: list: %w", e.cfg.Name, err)
	}
	return buildExecStream(e.cfg, res.Stdout, w)
}

// buildExecStream accepts either a JSON array of entries (oldest first) or
// an object {"baseline": "...", "changes": [...]}.
func buildExecStream(cfg config.Source, raw []byte, w Window) (model.Stream, error) {
	s := model.Stream{Source: cfg.Name, Kind: "exec"}
	var doc struct {
		Baseline string      `json:"baseline"`
		Changes  []execEntry `json:"changes"`
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &doc.Changes); err != nil {
			return s, fmt.Errorf("source %s: parse list output: %w", cfg.Name, err)
		}
	} else if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return s, fmt.Errorf("source %s: list must print a JSON array or {baseline, changes}: %w", cfg.Name, err)
	}

	type entry struct {
		execEntry
		t time.Time
	}
	entries := make([]entry, 0, len(doc.Changes))
	for i, c := range doc.Changes {
		if c.Version == "" {
			return s, fmt.Errorf("source %s: entry %d has no version", cfg.Name, i)
		}
		t, err := parseTime(c.Time)
		if err != nil {
			return s, fmt.Errorf("source %s: entry %s: %w", cfg.Name, c.Version, err)
		}
		entries = append(entries, entry{c, t.UTC()})
	}

	baseline := doc.Baseline
	start := 0
	if cfg.Good != "" {
		baseline = cfg.Good
		for i, en := range entries {
			if en.Version == cfg.Good {
				start = i + 1
			}
		}
	} else if !w.Since.IsZero() {
		for i, en := range entries {
			if en.t.After(w.Since) {
				break
			}
			baseline, start = en.Version, i+1
		}
	}
	if baseline == "" {
		return s, fmt.Errorf("source %s: no baseline; set good, window.since or print {\"baseline\": ...}", cfg.Name)
	}

	end := len(entries)
	if cfg.Bad != "" {
		end = -1
		for i, en := range entries {
			if en.Version == cfg.Bad {
				end = i + 1
			}
		}
		if end < 0 {
			return s, fmt.Errorf("source %s: bad version %q not found in list output", cfg.Name, cfg.Bad)
		}
	} else if !w.Until.IsZero() {
		for end > start && entries[end-1].t.After(w.Until) {
			end--
		}
	}

	s.Baseline, s.Head = baseline, baseline
	prev := baseline
	for _, en := range entries[start:max(start, end)] {
		s.Changes = append(s.Changes, model.Change{
			Source: cfg.Name, Kind: "exec", Version: en.Version, Prev: prev,
			Time: en.t, Author: en.Author, Summary: en.Summary,
		})
		prev = en.Version
		s.Head = en.Version
	}
	return s, nil
}

func (e *execSource) Render(ctx context.Context, version, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	_, err := execx.Shell(ctx, e.rt.Shell, execx.Cmd{
		Dir:    e.rt.BaseDir,
		Stdout: e.rt.Log, Stderr: e.rt.Log,
		Env: []string{
			"INFRABISECT_SOURCE=" + e.cfg.Name,
			"INFRABISECT_VERSION=" + version,
			"INFRABISECT_OUT_DIR=" + outDir,
		},
	}, e.cfg.Render.Command)
	if err != nil {
		return fmt.Errorf("source %s: render %s: %w", e.cfg.Name, version, err)
	}
	return nil
}

func (e *execSource) Remedy(c model.Change) model.Remedy {
	r := model.Remedy{Description: fmt.Sprintf("Undo %s on %s (back to %s)", c.Version, e.cfg.Name, c.Prev)}
	if e.cfg.Remedy != "" {
		r.Commands = []string{strings.NewReplacer(
			"{{version}}", c.Version, "{{prev}}", c.Prev, "{{source}}", c.Source,
		).Replace(e.cfg.Remedy)}
	}
	return r
}
