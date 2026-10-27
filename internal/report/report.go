// Package report renders bisect results for terminals, Markdown and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/pughal-prog/infrabisect/internal/model"
)

// Status values.
const (
	StatusCulprit         = "culprit-found"
	StatusInconclusive    = "inconclusive"
	StatusNoChanges       = "no-changes"
	StatusNotReproducible = "not-reproducible"
	StatusBaselineBad     = "baseline-bad"
	StatusError           = "error"
)

// Report is the complete, serializable result of a run.
type Report struct {
	Tool        string          `json:"tool"`
	Version     string          `json:"version"`
	Status      string          `json:"status"`
	Message     string          `json:"message"`
	StartedAt   time.Time       `json:"startedAt"`
	Duration    time.Duration   `json:"durationNs"`
	Since       *time.Time      `json:"since,omitempty"`
	Until       *time.Time      `json:"until,omitempty"`
	Environment string          `json:"environment"`
	Sources     []SourceSummary `json:"sources"`
	Timeline    []model.Change  `json:"timeline"`
	Steps       []Step          `json:"steps"`
	Culprit     *model.Change   `json:"culprit,omitempty"`
	Candidates  []model.Change  `json:"candidates,omitempty"`
	Remedy      *model.Remedy   `json:"remedy,omitempty"`
	Revert      string          `json:"revert,omitempty"`
	RevertError string          `json:"revertError,omitempty"`
}

// SourceSummary describes one source's slice of the timeline.
type SourceSummary struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Baseline string `json:"baseline"`
	Head     string `json:"head"`
	Changes  int    `json:"changes"`
}

// Step is one tested state.
type Step struct {
	N        int           `json:"n"`
	State    int           `json:"state"`
	After    *model.Change `json:"after,omitempty"` // last change applied; nil for the baseline
	Verdict  model.Verdict `json:"verdict"`
	Reason   string        `json:"reason"`
	Duration time.Duration `json:"durationNs"`
}

// New starts a report for a timeline.
func New(tl *model.Timeline, version, environment string) *Report {
	r := &Report{
		Tool: "infrabisect", Version: version, StartedAt: time.Now().UTC(),
		Environment: environment, Timeline: tl.Changes,
	}
	counts := tl.CountBySource()
	for _, name := range tl.Sources {
		r.Sources = append(r.Sources, SourceSummary{
			Name: name, Kind: tl.Kinds[name], Baseline: tl.Baseline[name],
			Head: tl.Heads[name], Changes: counts[name],
		})
	}
	if r.Timeline == nil {
		r.Timeline = []model.Change{}
	}
	return r
}

// SetWindow records the incident window.
func (r *Report) SetWindow(since, until time.Time) {
	if !since.IsZero() {
		s := since.UTC()
		r.Since = &s
	}
	if !until.IsZero() {
		u := until.UTC()
		r.Until = &u
	}
}

// AddStep records a tested state and returns it.
func (r *Report) AddStep(state int, v model.Verdict, reason string, d time.Duration) Step {
	s := Step{N: len(r.Steps) + 1, State: state, Verdict: v, Reason: reason, Duration: d}
	if state > 0 && state <= len(r.Timeline) {
		c := r.Timeline[state-1]
		s.After = &c
	}
	r.Steps = append(r.Steps, s)
	return s
}

// Finish records the total duration.
func (r *Report) Finish(d time.Duration) { r.Duration = d }

// ExitCode maps the status to a process exit code: 0 culprit found,
// 2 inconclusive or not reproducible, 3 no changes, 1 errors.
func (r *Report) ExitCode() int {
	switch r.Status {
	case StatusCulprit:
		return 0
	case StatusInconclusive, StatusNotReproducible, StatusBaselineBad:
		return 2
	case StatusNoChanges:
		return 3
	}
	return 1
}

// verdicts maps tested states to their verdicts.
func (r *Report) verdicts() map[int]model.Verdict {
	m := map[int]model.Verdict{}
	for _, s := range r.Steps {
		m[s.State] = s.Verdict
	}
	return m
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Round formats a duration compactly.
func Round(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

type palette struct{ red, green, yellow, cyan, bold, dim, reset string }

func colors(on bool) palette {
	if !on {
		return palette{}
	}
	return palette{"\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[36m", "\x1b[1m", "\x1b[2m", "\x1b[0m"}
}

// WriteText writes the human summary printed at the end of a run.
func (r *Report) WriteText(w io.Writer, color bool) {
	p := colors(color)
	fmt.Fprintln(w)
	switch r.Status {
	case StatusCulprit:
		c := r.Culprit
		fmt.Fprintf(w, "%s%s✖ Culprit found%s after %d steps in %s\n\n", p.bold, p.red, p.reset, len(r.Steps), Round(r.Duration))
		fmt.Fprintf(w, "  change   %d of %d\n", c.Index, len(r.Timeline))
		fmt.Fprintf(w, "  source   %s (%s)\n", c.Source, c.Kind)
		fmt.Fprintf(w, "  version  %s%s%s", p.bold, c.Version, p.reset)
		if c.Prev != "" {
			fmt.Fprintf(w, "  %s(was %s)%s", p.dim, model.Short(c.Prev), p.reset)
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  when     %s\n", c.Time.Format("2006-01-02 15:04:05 MST"))
		if c.Author != "" {
			fmt.Fprintf(w, "  author   %s\n", c.Author)
		}
		fmt.Fprintf(w, "  summary  %s\n", c.Summary)
		if s := r.lastBadReason(); s != "" {
			fmt.Fprintf(w, "  failure  %s\n", s)
		}
		if r.Remedy != nil {
			fmt.Fprintf(w, "\n%sFix:%s %s\n", p.bold, p.reset, r.Remedy.Description)
			for _, cmd := range r.Remedy.Commands {
				fmt.Fprintf(w, "  %s$ %s%s\n", p.cyan, cmd, p.reset)
			}
		}
		if r.Revert != "" {
			fmt.Fprintf(w, "\n%sRevert prepared:%s %s\n", p.bold, p.reset, r.Revert)
		}
		if r.RevertError != "" {
			fmt.Fprintf(w, "\n%sRevert not prepared:%s %s\n", p.yellow, p.reset, r.RevertError)
		}
	case StatusInconclusive:
		fmt.Fprintf(w, "%s%s? Inconclusive:%s %s\n", p.bold, p.yellow, p.reset, r.Message)
		for _, c := range r.Candidates {
			fmt.Fprintf(w, "  - %d  %s  %s\n", c.Index, c.Ref(), c.Summary)
		}
	case StatusNoChanges:
		fmt.Fprintf(w, "%sNothing to bisect:%s %s\n", p.yellow, p.reset, r.Message)
	case StatusError:
		// The CLI prints the error itself; the timeline below gives context.
		fmt.Fprintf(w, "%s%sBisect aborted%s after %d steps\n", p.bold, p.red, p.reset, len(r.Steps))
	default:
		fmt.Fprintf(w, "%s%s%s:%s %s\n", p.bold, p.yellow, r.Status, p.reset, r.Message)
	}
	if len(r.Timeline) > 0 && len(r.Timeline) <= 60 {
		fmt.Fprintf(w, "\n%sTimeline%s  %s✔ good  ✖ bad  ~ skipped  ▶ culprit%s\n", p.bold, p.reset, p.dim, p.reset)
		r.writeTimeline(w, p)
	}
}

func (r *Report) lastBadReason() string {
	if r.Culprit == nil {
		return ""
	}
	for _, s := range r.Steps {
		if s.State == r.Culprit.Index && s.Verdict == model.Bad {
			return s.Reason
		}
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Verdict == model.Bad {
			return r.Steps[i].Reason
		}
	}
	return ""
}

func (r *Report) writeTimeline(w io.Writer, p palette) {
	v := r.verdicts()
	width := 0
	for _, c := range r.Timeline {
		if n := len(c.Source); n > width {
			width = n
		}
	}
	for _, c := range r.Timeline {
		mark, col := "·", p.dim
		if verdict, ok := v[c.Index]; ok {
			switch verdict {
			case model.Good:
				mark, col = "✔", p.green
			case model.Bad:
				mark, col = "✖", p.red
			default:
				mark, col = "~", p.yellow
			}
		}
		if r.Culprit != nil && c.Index == r.Culprit.Index {
			mark, col = "▶", p.bold+p.red
		}
		fmt.Fprintf(w, "  %s%s %3d  %s  %-*s  %-12s  %s%s\n", col, mark, c.Index,
			c.Time.Format("01-02 15:04"), width, c.Source, c.ShortVersion(), truncate(c.Summary, 60), p.reset)
	}
}

// WriteMarkdown writes a report suitable for an incident ticket or PR.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "# InfraBisect report\n\n")
	fmt.Fprintf(w, "| | |\n|---|---|\n")
	fmt.Fprintf(w, "| Status | **%s** |\n", r.Status)
	fmt.Fprintf(w, "| Result | %s |\n", escape(r.Message))
	fmt.Fprintf(w, "| Changes | %d across %d sources |\n", len(r.Timeline), len(r.Sources))
	fmt.Fprintf(w, "| Steps | %d in %s |\n", len(r.Steps), Round(r.Duration))
	fmt.Fprintf(w, "| Environment | `%s` |\n", r.Environment)
	if r.Since != nil {
		fmt.Fprintf(w, "| Since | %s |\n", r.Since.Format(time.RFC3339))
	}
	if r.Until != nil {
		fmt.Fprintf(w, "| Until | %s |\n", r.Until.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "| Generated | %s by infrabisect %s |\n\n", r.StartedAt.Format(time.RFC3339), r.Version)

	if c := r.Culprit; c != nil {
		fmt.Fprintf(w, "## Culprit\n\n")
		fmt.Fprintf(w, "| | |\n|---|---|\n")
		fmt.Fprintf(w, "| Change | %d of %d |\n", c.Index, len(r.Timeline))
		fmt.Fprintf(w, "| Source | `%s` (%s) |\n", c.Source, c.Kind)
		fmt.Fprintf(w, "| Version | `%s` (previously `%s`) |\n", c.Version, c.Prev)
		fmt.Fprintf(w, "| Time | %s |\n", c.Time.Format(time.RFC3339))
		if c.Author != "" {
			fmt.Fprintf(w, "| Author | %s |\n", escape(c.Author))
		}
		fmt.Fprintf(w, "| Summary | %s |\n", escape(c.Summary))
		if s := r.lastBadReason(); s != "" {
			fmt.Fprintf(w, "| Failure | `%s` |\n", strings.ReplaceAll(s, "`", "'"))
		}
		fmt.Fprintln(w)
		if r.Remedy != nil {
			fmt.Fprintf(w, "### Fix\n\n%s\n\n", r.Remedy.Description)
			if len(r.Remedy.Commands) > 0 {
				fmt.Fprintf(w, "```bash\n%s\n```\n\n", strings.Join(r.Remedy.Commands, "\n"))
			}
		}
		if r.Revert != "" {
			fmt.Fprintf(w, "Revert prepared: %s\n\n", r.Revert)
		}
	}
	if len(r.Candidates) > 0 {
		fmt.Fprintf(w, "## Candidates\n\nUntestable states prevented narrowing to one change:\n\n")
		for _, c := range r.Candidates {
			fmt.Fprintf(w, "- **%d** `%s` %s\n", c.Index, c.Ref(), escape(c.Summary))
		}
		fmt.Fprintln(w)
	}

	if len(r.Steps) > 0 {
		fmt.Fprintf(w, "## Steps\n\n| # | State | After change | Verdict | Time | Reason |\n|---|---|---|---|---|---|\n")
		for _, s := range r.Steps {
			after := "baseline"
			if s.After != nil {
				after = "`" + s.After.Ref() + "`"
			}
			fmt.Fprintf(w, "| %d | %d/%d | %s | %s | %s | %s |\n", s.N, s.State, len(r.Timeline), after, s.Verdict, Round(s.Duration), escape(truncate(s.Reason, 120)))
		}
		fmt.Fprintln(w)
	}

	if len(r.Sources) > 0 {
		fmt.Fprintf(w, "## Sources\n\n| Source | Kind | Good | Bad | Changes |\n|---|---|---|---|---|\n")
		srcs := append([]SourceSummary(nil), r.Sources...)
		sort.SliceStable(srcs, func(i, j int) bool { return srcs[i].Changes > srcs[j].Changes })
		for _, s := range srcs {
			fmt.Fprintf(w, "| `%s` | %s | `%s` | `%s` | %d |\n", s.Name, s.Kind, model.Short(s.Baseline), model.Short(s.Head), s.Changes)
		}
		fmt.Fprintln(w)
	}

	if len(r.Timeline) > 0 {
		v := r.verdicts()
		fmt.Fprintf(w, "## Timeline\n\n| # | Time (UTC) | Source | Version | Summary | Tested |\n|---|---|---|---|---|---|\n")
		for _, c := range r.Timeline {
			tested := ""
			if verdict, ok := v[c.Index]; ok {
				tested = verdict.String()
			}
			idx := fmt.Sprint(c.Index)
			if r.Culprit != nil && c.Index == r.Culprit.Index {
				idx, tested = "**"+idx+"** ▶", "**culprit**"
			}
			fmt.Fprintf(w, "| %s | %s | `%s` | `%s` | %s | %s |\n", idx, c.Time.UTC().Format("2006-01-02 15:04"), c.Source, c.ShortVersion(), escape(truncate(c.Summary, 80)), tested)
		}
	}
}

// WriteTimeline prints a timeline without bisect results.
func WriteTimeline(w io.Writer, tl *model.Timeline, color bool) {
	p := colors(color)
	r := New(tl, "", "")
	fmt.Fprintf(w, "%s%d changes%s across %d sources\n", p.bold, tl.Len(), p.reset, len(tl.Sources))
	for _, s := range r.Sources {
		fmt.Fprintf(w, "  %-20s %-5s good %-12s bad %-12s %d changes\n", s.Name, s.Kind, model.Short(s.Baseline), model.Short(s.Head), s.Changes)
	}
	if tl.Len() > 0 {
		fmt.Fprintln(w)
		r.writeTimeline(w, p)
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func escape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
