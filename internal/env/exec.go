package env

import (
	"context"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// execEnv delegates to user hooks: docker compose, a cloud sandbox account,
// terraform apply against LocalStack, a Nomad dev agent, and so on.
type execEnv struct {
	cfg  config.Environment
	opts Options
}

func (e *execEnv) Name() string  { return "exec" }
func (e *execEnv) Env() []string { return nil }

func (e *execEnv) hook(ctx context.Context, script string, env []string) error {
	if script == "" {
		return nil
	}
	env = append(env, "INFRABISECT_WORK_DIR="+e.opts.WorkDir)
	_, err := execx.Shell(ctx, e.opts.Shell, execx.Cmd{
		Dir: e.opts.BaseDir, Env: env, Stdout: e.opts.Log, Stderr: e.opts.Log,
	}, script)
	return err
}

func (e *execEnv) Prepare(ctx context.Context) error { return e.hook(ctx, e.cfg.Setup, nil) }
func (e *execEnv) Apply(ctx context.Context, st model.State) error {
	return e.hook(ctx, e.cfg.Apply, st.Env())
}
func (e *execEnv) Reset(ctx context.Context, st model.State) error {
	return e.hook(ctx, e.cfg.Reset, st.Env())
}
func (e *execEnv) Cleanup(ctx context.Context) error { return e.hook(ctx, e.cfg.Teardown, nil) }
