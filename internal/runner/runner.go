// Package runner wires sources, the environment and checks into a bisect.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pughal-prog/infrabisect/internal/bisect"
	"github.com/pughal-prog/infrabisect/internal/check"
	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/env"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
	"github.com/pughal-prog/infrabisect/internal/report"
	"github.com/pughal-prog/infrabisect/internal/source"
)

// Runner owns one configured bisect.
type Runner struct {
	Cfg      *config.Config
	Sources  []source.Source
	Env      env.Environment
	Checks   []check.Checker
	WorkDir  string
	Progress io.Writer // human-readable progress
	LogPath  string

	log     *os.File
	logSink io.Writer
}

// New builds a runner. Tool output goes to <workDir>/run.log, and also to
// progress when verbose is set.
func New(cfg *config.Config, progress io.Writer, verbose bool) (*Runner, error) {
	if progress == nil {
		progress = io.Discard
	}
	workDir := cfg.Resolve(cfg.WorkDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(workDir, "run.log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	var sink io.Writer = f
	if verbose {
		sink = io.MultiWriter(f, progress)
	}
	shell := cfg.Shell
	if len(shell) == 0 {
		shell = execx.DefaultShell()
	}

	r := &Runner{Cfg: cfg, WorkDir: workDir, Progress: progress, LogPath: logPath, log: f, logSink: sink}
	rt := source.Runtime{BaseDir: cfg.BaseDir, CacheDir: filepath.Join(workDir, "cache"), Shell: shell, Log: sink}
	for _, sc := range cfg.Sources {
		s, err := source.New(sc, rt)
		if err != nil {
			f.Close()
			return nil, err
		}
		r.Sources = append(r.Sources, s)
	}
	r.Env, err = env.New(cfg.Environment, env.Options{BaseDir: cfg.BaseDir, WorkDir: workDir, Shell: shell, Log: sink})
	if err != nil {
		f.Close()
		return nil, err
	}
	for _, cc := range cfg.Checks {
		c, err := check.New(cc, check.Options{BaseDir: cfg.BaseDir, Shell: shell, Log: sink})
		if err != nil {
			f.Close()
			return nil, err
		}
		r.Checks = append(r.Checks, c)
	}
	return r, nil
}

// Close releases the log file.
func (r *Runner) Close() error { return r.log.Close() }

// Window resolves the incident window; non-empty overrides win over config.
func (r *Runner) Window(since, until string) (source.Window, error) {
	if since == "" {
		since = r.Cfg.Window.Since
	}
	if until == "" {
		until = r.Cfg.Window.Until
	}
	now := time.Now()
	s, err := config.ParseTime(since, now)
	if err != nil {
		return source.Window{}, fmt.Errorf("since: %w", err)
	}
	u, err := config.ParseTime(until, now)
	if err != nil {
		return source.Window{}, fmt.Errorf("until: %w", err)
	}
	if !s.IsZero() && !u.IsZero() && !u.After(s) {
		return source.Window{}, errors.New("until must be after since")
	}
	return source.Window{Since: s, Until: u}, nil
}

// Timeline collects every source's stream and merges them.
func (r *Runner) Timeline(ctx context.Context, w source.Window) (*model.Timeline, error) {
	streams := make([]model.Stream, 0, len(r.Sources))
	for _, s := range r.Sources {
		st, err := s.Stream(ctx, w)
		if err != nil {
			return nil, err
		}
		streams = append(streams, st)
	}
	return model.Merge(streams), nil
}

// Materialize renders state k of the timeline into dir.
func (r *Runner) Materialize(ctx context.Context, tl *model.Timeline, k int, dir string) (model.State, error) {
	snap := tl.At(k)
	st := model.State{Index: k, Total: tl.Len(), Snapshot: snap, Dir: dir}
	if err := os.RemoveAll(dir); err != nil {
		return st, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}
	for _, s := range r.Sources {
		if err := s.Render(ctx, snap[s.Name()], filepath.Join(dir, s.Name())); err != nil {
			return st, fmt.Errorf("render %s@%s: %w", s.Name(), model.Short(snap[s.Name()]), err)
		}
	}
	return st, nil
}

// Options controls a run.
type Options struct {
	VerifyGood bool
	VerifyBad  bool
	KeepStates bool
	Revert     bool
	Version    string
}

// Run bisects the timeline and returns a report. The error is non-nil only
// for failures of InfraBisect itself (interrupts, broken environment); a
// search that cannot conclude is reported through Report.Status.
func (r *Runner) Run(ctx context.Context, tl *model.Timeline, w source.Window, opts Options) (*report.Report, error) {
	rep := report.New(tl, opts.Version, r.Env.Name())
	rep.SetWindow(w.Since, w.Until)
	started := time.Now()
	defer func() { rep.Finish(time.Since(started)) }()

	if tl.Len() == 0 {
		rep.Status, rep.Message = report.StatusNoChanges, bisect.ErrNoChanges.Error()
		return rep, nil
	}

	verify := ""
	if opts.VerifyBad {
		verify = ", +1 to confirm the failure"
	}
	fmt.Fprintf(r.Progress, "Bisecting %d changes across %d sources in %s (about %d steps%s)\n",
		tl.Len(), len(tl.Sources), r.Env.Name(), bisect.MaxSteps(tl.Len()), verify)

	// Cleanup is registered before Prepare so a half-prepared environment
	// (cluster created, image load failed) is still torn down.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := r.Env.Cleanup(cctx); err != nil {
			fmt.Fprintf(r.Progress, "warning: environment cleanup failed: %v\n", err)
		}
		if !opts.KeepStates && !r.Cfg.Bisect.KeepStates {
			_ = os.RemoveAll(filepath.Join(r.WorkDir, "states"))
		}
	}()
	fmt.Fprintf(r.Progress, "Preparing environment %s...\n", r.Env.Name())
	if err := r.Env.Prepare(ctx); err != nil {
		rep.Status, rep.Message = report.StatusError, "prepare environment: "+err.Error()
		return rep, fmt.Errorf("prepare environment: %w", err)
	}

	verdictFor := func(policy string) model.Verdict {
		if policy == "skip" {
			return model.Skip
		}
		return model.Bad
	}

	test := func(ctx context.Context, k int) (model.Verdict, error) {
		t0 := time.Now()
		fmt.Fprintf(r.logSink, "\n===== state %d/%d =====\n", k, tl.Len())
		dir := filepath.Join(r.WorkDir, "states", fmt.Sprintf("state-%04d", k))
		var v model.Verdict
		var reason string

		st, err := r.Materialize(ctx, tl, k, dir)
		switch {
		case err != nil:
			v, reason = verdictFor(r.Cfg.Bisect.OnRenderError), err.Error()
		default:
			if err := r.Env.Apply(ctx, st); err != nil {
				v, reason = verdictFor(r.Cfg.Bisect.OnApplyError), "apply: "+err.Error()
			} else {
				v, reason = check.All(ctx, r.Checks, st, r.Env.Env())
			}
			if err := r.Env.Reset(context.WithoutCancel(ctx), st); err != nil {
				return v, fmt.Errorf("reset environment after state %d: %w", k, err)
			}
		}
		if ctx.Err() != nil {
			return model.Skip, ctx.Err()
		}
		step := rep.AddStep(k, v, reason, time.Since(t0))
		r.printStep(step, tl.Len())
		return v, nil
	}

	res, err := bisect.Search(ctx, tl.Len(), test, bisect.Options{VerifyGood: opts.VerifyGood, VerifyBad: opts.VerifyBad})
	switch {
	case errors.Is(err, bisect.ErrBadIsGood):
		rep.Status, rep.Message = report.StatusNotReproducible, err.Error()
		return rep, nil
	case errors.Is(err, bisect.ErrBaselineIsBad):
		rep.Status, rep.Message = report.StatusBaselineBad, err.Error()
		return rep, nil
	case err != nil:
		rep.Status, rep.Message = report.StatusError, err.Error()
		return rep, err
	}

	if res.FirstBad == 0 {
		rep.Status = report.StatusInconclusive
		rep.Message = fmt.Sprintf("untestable states left %d candidate changes", len(res.Candidates))
		for _, k := range res.Candidates {
			rep.Candidates = append(rep.Candidates, tl.Changes[k-1])
		}
		return rep, nil
	}

	culprit := tl.Changes[res.FirstBad-1]
	rep.Status = report.StatusCulprit
	rep.Culprit = &culprit
	rep.Message = fmt.Sprintf("change %d/%d (%s) broke the checks", culprit.Index, tl.Len(), culprit.Ref())
	for _, s := range r.Sources {
		if s.Name() != culprit.Source {
			continue
		}
		rem := s.Remedy(culprit)
		rep.Remedy = &rem
		if opts.Revert {
			if rv, ok := s.(source.Reverter); ok {
				out, err := rv.Revert(ctx, culprit, tl.Heads[culprit.Source])
				if err != nil {
					rep.RevertError = err.Error()
				} else {
					rep.Revert = out
				}
			} else {
				rep.RevertError = fmt.Sprintf("%s sources cannot prepare reverts automatically; run the remedy commands", s.Kind())
			}
		}
	}
	return rep, nil
}

func (r *Runner) printStep(s report.Step, total int) {
	after := "baseline"
	if s.After != nil {
		after = "after " + s.After.Ref()
	}
	fmt.Fprintf(r.Progress, "  step %-2d state %*d/%d  %-28s %-5s %6s  %s\n",
		s.N, len(fmt.Sprint(total)), s.State, total, after, s.Verdict, report.Round(s.Duration), s.Reason)
}
