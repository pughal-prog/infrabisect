// Package check decides whether a reproduced state is good or bad.
package check

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// SkipExitCode marks a state as untestable, as with `git bisect run`.
const SkipExitCode = 125

// Checker evaluates one state.
type Checker interface {
	Name() string
	// Check returns the verdict and a one-line reason. env holds variables
	// from the state and the environment (for example KUBECONFIG).
	Check(ctx context.Context, st model.State, env []string) (model.Verdict, string)
}

// Options carries runtime settings for checks.
type Options struct {
	BaseDir string
	Shell   []string
	Log     io.Writer
}

// New builds a checker from configuration.
func New(cfg config.Check, opts Options) (Checker, error) {
	switch cfg.Type {
	case "command":
		return &commandCheck{cfg: cfg, opts: opts}, nil
	case "http":
		return &httpCheck{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout.Or(10 * time.Second)}}, nil
	case "rollout":
		return &rolloutCheck{cfg: cfg, opts: opts}, nil
	}
	return nil, fmt.Errorf("unknown check type %q", cfg.Type)
}

// All runs checks in order. The first bad or skip verdict wins.
func All(ctx context.Context, checks []Checker, st model.State, env []string) (model.Verdict, string) {
	for _, c := range checks {
		v, reason := c.Check(ctx, st, env)
		if v != model.Good {
			return v, c.Name() + ": " + reason
		}
	}
	return model.Good, "all checks passed"
}

// retry runs attempt up to 1+retries times, returning on the first good or
// skip verdict and waiting interval between attempts.
func retry(ctx context.Context, cfg config.Check, attempt func(context.Context) (model.Verdict, string)) (model.Verdict, string) {
	interval := cfg.Interval.Or(5 * time.Second)
	var v model.Verdict
	var reason string
	for i := 0; i <= cfg.Retries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return model.Skip, "interrupted"
			case <-time.After(interval):
			}
		}
		v, reason = attempt(ctx)
		if v != model.Bad {
			return v, reason
		}
	}
	if cfg.Retries > 0 {
		reason = fmt.Sprintf("%s (after %d attempts)", reason, cfg.Retries+1)
	}
	return v, reason
}

type commandCheck struct {
	cfg  config.Check
	opts Options
}

func (c *commandCheck) Name() string { return c.cfg.Name }

func (c *commandCheck) Check(ctx context.Context, st model.State, env []string) (model.Verdict, string) {
	return retry(ctx, c.cfg, func(ctx context.Context) (model.Verdict, string) {
		ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout.Or(5*time.Minute))
		defer cancel()
		res, err := execx.Shell(ctx, c.opts.Shell, execx.Cmd{
			Dir: c.opts.BaseDir, Env: append(st.Env(), env...), Stdout: c.opts.Log, Stderr: c.opts.Log,
		}, c.cfg.Run)
		if err == nil {
			return model.Good, "exit 0"
		}
		if ctx.Err() == context.DeadlineExceeded {
			return model.Bad, "timed out after " + c.cfg.Timeout.Or(5*time.Minute).String()
		}
		if res.ExitCode == SkipExitCode {
			return model.Skip, "exit 125 (untestable)"
		}
		return model.Bad, fmt.Sprintf("exit %d: %s", res.ExitCode, lastLine(res.Combined()))
	})
}

type httpCheck struct {
	cfg    config.Check
	client *http.Client
}

func (h *httpCheck) Name() string { return h.cfg.Name }

func (h *httpCheck) Check(ctx context.Context, _ model.State, _ []string) (model.Verdict, string) {
	return retry(ctx, h.cfg, func(ctx context.Context) (model.Verdict, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.URL, nil)
		if err != nil {
			return model.Skip, err.Error()
		}
		resp, err := h.client.Do(req)
		if err != nil {
			return model.Bad, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != h.cfg.ExpectStatus {
			return model.Bad, fmt.Sprintf("status %d, want %d", resp.StatusCode, h.cfg.ExpectStatus)
		}
		if h.cfg.BodyContains != "" && !strings.Contains(string(body), h.cfg.BodyContains) {
			return model.Bad, fmt.Sprintf("body does not contain %q", h.cfg.BodyContains)
		}
		return model.Good, fmt.Sprintf("status %d", resp.StatusCode)
	})
}

// rolloutCheck waits for every Deployment, StatefulSet and DaemonSet to
// finish rolling out.
type rolloutCheck struct {
	cfg  config.Check
	opts Options
}

func (r *rolloutCheck) Name() string { return r.cfg.Name }

func (r *rolloutCheck) Check(ctx context.Context, st model.State, env []string) (model.Verdict, string) {
	timeout := r.cfg.Timeout.Or(3 * time.Minute)
	scopes := [][]string{{"--all-namespaces"}}
	if len(r.cfg.Namespaces) > 0 {
		scopes = nil
		for _, ns := range r.cfg.Namespaces {
			scopes = append(scopes, []string{"--namespace", ns})
		}
	}
	return retry(ctx, r.cfg, func(ctx context.Context) (model.Verdict, string) {
		deadline := time.Now().Add(timeout)
		for _, scope := range scopes {
			// Deployments: condition Available. StatefulSets/DaemonSets: rollout status.
			left := time.Until(deadline)
			if left <= 0 {
				return model.Bad, "rollout timed out"
			}
			args := append([]string{"wait", "deployments", "--all", "--for=condition=Available", "--timeout=" + left.Round(time.Second).String()}, scope...)
			if v, reason := r.kubectl(ctx, env, args); v != model.Good {
				return v, reason
			}
			for _, kind := range []string{"statefulsets", "daemonsets"} {
				v, reason := r.rolloutAll(ctx, env, kind, scope, deadline)
				if v != model.Good {
					return v, reason
				}
			}
		}
		return model.Good, "all workloads rolled out"
	})
}

func (r *rolloutCheck) kubectl(ctx context.Context, env []string, args []string) (model.Verdict, string) {
	res, err := execx.Run(ctx, execx.Cmd{Name: "kubectl", Args: args, Env: env, Dir: r.opts.BaseDir, Stdout: r.opts.Log, Stderr: r.opts.Log})
	if err == nil {
		return model.Good, "ok"
	}
	out := res.Combined()
	if strings.Contains(out, "no matching resources found") {
		return model.Good, "no workloads"
	}
	return model.Bad, lastLine(out)
}

func (r *rolloutCheck) rolloutAll(ctx context.Context, env []string, kind string, scope []string, deadline time.Time) (model.Verdict, string) {
	list := append([]string{"get", kind, "-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}"}, scope...)
	res, err := execx.Run(ctx, execx.Cmd{Name: "kubectl", Args: list, Env: env, Dir: r.opts.BaseDir})
	if err != nil {
		return model.Bad, lastLine(res.Combined())
	}
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		ns, name, ok := strings.Cut(strings.TrimSpace(line), "/")
		if !ok {
			continue
		}
		left := time.Until(deadline)
		if left <= 0 {
			return model.Bad, "rollout timed out"
		}
		args := []string{"rollout", "status", kind + "/" + name, "-n", ns, "--timeout=" + left.Round(time.Second).String()}
		if v, reason := r.kubectl(ctx, env, args); v != model.Good {
			return v, kind + "/" + name + ": " + reason
		}
	}
	return model.Good, "ok"
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
