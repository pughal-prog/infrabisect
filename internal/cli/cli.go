// Package cli implements the infrabisect command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pughalan/infrabisect/internal/config"
	"github.com/pughalan/infrabisect/internal/report"
	"github.com/pughalan/infrabisect/internal/runner"
)

// IO holds the process streams.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
}

// BuildInfo identifies the binary.
type BuildInfo struct {
	Version string
	Commit  string
}

const usage = `InfraBisect - git bisect for production infrastructure.

Finds the one change that broke your system by binary-searching a merged
timeline of git commits, Helm revisions and custom change sources, replaying
each candidate state in a throwaway environment.

Usage:
  infrabisect <command> [flags]

Commands:
  run        Bisect the timeline and report the culprit change
  timeline   Show the merged change timeline without testing anything
  render     Materialize one state of the timeline to a directory
  validate   Check the configuration file
  init       Write a starter infrabisect.yaml
  version    Print the version

Run "infrabisect <command> -h" for command flags.
Docs: https://github.com/pughalan/infrabisect
`

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, stdio IO, build BuildInfo) int {
	if len(args) == 0 {
		fmt.Fprint(stdio.Stderr, usage)
		return 1
	}
	cmd, rest := args[0], args[1:]
	var err error
	code := 0
	switch cmd {
	case "run":
		code, err = cmdRun(ctx, rest, stdio, build)
	case "timeline", "log":
		err = cmdTimeline(ctx, rest, stdio)
	case "render":
		err = cmdRender(ctx, rest, stdio)
	case "validate":
		err = cmdValidate(rest, stdio)
	case "init":
		err = cmdInit(rest, stdio)
	case "version", "--version", "-v":
		fmt.Fprintf(stdio.Stdout, "infrabisect %s (%s)\n", build.Version, build.Commit)
	case "help", "-h", "--help":
		fmt.Fprint(stdio.Stdout, usage)
	default:
		fmt.Fprintf(stdio.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 1
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stdio.Stderr, "error: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// common holds flags shared by commands that load a configuration.
type common struct {
	config  string
	since   string
	until   string
	verbose bool
}

func (c *common) register(fs *flag.FlagSet, window bool) {
	fs.StringVar(&c.config, "c", config.DefaultFile, "configuration file")
	fs.StringVar(&c.config, "config", config.DefaultFile, "configuration file")
	if window {
		fs.StringVar(&c.since, "since", "", "start of the incident window (RFC 3339, \"2006-01-02 15:04\" or a duration like 6h)")
		fs.StringVar(&c.until, "until", "", "end of the incident window")
	}
	fs.BoolVar(&c.verbose, "verbose", false, "stream tool output to stderr")
}

func newFlagSet(name, summary string, stdio IO) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stdio.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(stdio.Stderr, "Usage: infrabisect %s [flags]\n\n%s\n\nFlags:\n", name, summary)
		fs.PrintDefaults()
	}
	return fs
}

func load(c common, stdio IO) (*runner.Runner, error) {
	cfg, err := config.Load(c.config)
	if err != nil {
		return nil, err
	}
	return runner.New(cfg, stdio.Stderr, c.verbose)
}

func cmdRun(ctx context.Context, args []string, stdio IO, build BuildInfo) (int, error) {
	fs := newFlagSet("run", "Bisect the timeline and report the change that broke the checks.", stdio)
	var c common
	c.register(fs, true)
	mdPath := fs.String("report", "", "write a Markdown report to this file")
	jsonPath := fs.String("json", "", "write a JSON report to this file (- for stdout)")
	verifyGood := fs.Bool("verify-good", false, "also test the good baseline before searching")
	noVerify := fs.Bool("no-verify", false, "do not test the bad state before searching")
	keep := fs.Bool("keep-states", false, "keep rendered states in the work directory")
	revert := fs.Bool("revert", false, "prepare a revert branch for a git culprit (never pushes)")
	noColor := fs.Bool("no-color", false, "disable colored output")
	if err := fs.Parse(args); err != nil {
		return 1, err
	}
	r, err := load(c, stdio)
	if err != nil {
		return 1, err
	}
	defer r.Close()

	w, err := r.Window(c.since, c.until)
	if err != nil {
		return 1, err
	}
	tl, err := r.Timeline(ctx, w)
	if err != nil {
		return 1, err
	}
	opts := runner.Options{
		VerifyGood: *verifyGood || r.Cfg.Bisect.VerifyGood,
		VerifyBad:  *r.Cfg.Bisect.VerifyBad && !*noVerify,
		KeepStates: *keep,
		Revert:     *revert,
		Version:    build.Version,
	}
	rep, runErr := r.Run(ctx, tl, w, opts)

	out := stdio.Stdout
	if *jsonPath == "-" {
		out = stdio.Stderr
	}
	rep.WriteText(out, useColor(out, *noColor))
	if *mdPath != "" {
		if err := writeFile(*mdPath, rep.WriteMarkdown); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "\nMarkdown report: %s\n", *mdPath)
	}
	switch *jsonPath {
	case "":
	case "-":
		if err := rep.WriteJSON(stdio.Stdout); err != nil {
			return 1, err
		}
	default:
		if err := writeFile(*jsonPath, func(w io.Writer) { _ = rep.WriteJSON(w) }); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "JSON report: %s\n", *jsonPath)
	}
	if runErr != nil {
		return 1, fmt.Errorf("%w (tool output in %s)", runErr, r.LogPath)
	}
	return rep.ExitCode(), nil
}

func cmdTimeline(ctx context.Context, args []string, stdio IO) error {
	fs := newFlagSet("timeline", "Show the merged change timeline across all sources.", stdio)
	var c common
	c.register(fs, true)
	asJSON := fs.Bool("json", false, "print JSON")
	noColor := fs.Bool("no-color", false, "disable colored output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := load(c, stdio)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := r.Window(c.since, c.until)
	if err != nil {
		return err
	}
	tl, err := r.Timeline(ctx, w)
	if err != nil {
		return err
	}
	if *asJSON {
		rep := report.New(tl, "", "")
		return rep.WriteJSON(stdio.Stdout)
	}
	report.WriteTimeline(stdio.Stdout, tl, useColor(stdio.Stdout, *noColor))
	return nil
}

func cmdRender(ctx context.Context, args []string, stdio IO) error {
	fs := newFlagSet("render", "Materialize one state of the timeline (0 = good baseline, N = bad) for inspection.", stdio)
	var c common
	c.register(fs, true)
	state := fs.Int("state", -1, "state to render (0..N); defaults to N")
	out := fs.String("out", "", "output directory (default <workDir>/render/state-N)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := load(c, stdio)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := r.Window(c.since, c.until)
	if err != nil {
		return err
	}
	tl, err := r.Timeline(ctx, w)
	if err != nil {
		return err
	}
	k := *state
	if k < 0 {
		k = tl.Len()
	}
	if k > tl.Len() {
		return fmt.Errorf("state %d out of range: the timeline has states 0..%d", k, tl.Len())
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(r.WorkDir, "render", fmt.Sprintf("state-%04d", k))
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	st, err := r.Materialize(ctx, tl, k, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Stdout, "Rendered state %d/%d to %s\n  %s\n", k, tl.Len(), dir, st.Snapshot)
	return nil
}

func cmdValidate(args []string, stdio IO) error {
	fs := newFlagSet("validate", "Check the configuration file for errors.", stdio)
	var c common
	c.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(c.config)
	if err != nil {
		return err
	}
	kinds := make([]string, len(cfg.Sources))
	for i, s := range cfg.Sources {
		kinds[i] = s.Name + "(" + s.Type + ")"
	}
	fmt.Fprintf(stdio.Stdout, "%s is valid: sources %s, environment %s, %d checks\n",
		c.config, strings.Join(kinds, ", "), cfg.Environment.Type, len(cfg.Checks))
	return nil
}

func cmdInit(args []string, stdio IO) error {
	fs := newFlagSet("init", "Write a starter configuration. Templates: "+strings.Join(templateNames(), ", ")+".", stdio)
	tmpl := fs.String("template", "kind", "template to use")
	out := fs.String("o", config.DefaultFile, "output file")
	force := fs.Bool("force", false, "overwrite an existing file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	body, ok := templates[*tmpl]
	if !ok {
		return fmt.Errorf("unknown template %q (available: %s)", *tmpl, strings.Join(templateNames(), ", "))
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s already exists; use -force to overwrite", *out)
	}
	if err := os.WriteFile(*out, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Stdout, "Wrote %s (%s template). Edit it, then run:\n  infrabisect validate\n  infrabisect timeline\n  infrabisect run --report incident.md\n", *out, *tmpl)
	return nil
}

func writeFile(path string, write func(io.Writer)) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	write(f)
	return f.Close()
}

func useColor(w io.Writer, disabled bool) bool {
	if disabled || os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
