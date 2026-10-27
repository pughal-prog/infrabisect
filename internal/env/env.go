// Package env reproduces a materialized state somewhere checks can run.
package env

import (
	"context"
	"fmt"
	"io"

	"github.com/pughalan/infrabisect/internal/config"
	"github.com/pughalan/infrabisect/internal/model"
)

// Environment reproduces states. Prepare and Cleanup run once per bisect;
// Apply and Reset run around the checks of every state.
type Environment interface {
	Name() string
	Prepare(ctx context.Context) error
	Apply(ctx context.Context, st model.State) error
	Reset(ctx context.Context, st model.State) error
	Cleanup(ctx context.Context) error
	// Env returns variables checks need to reach the environment.
	Env() []string
}

// Options carries runtime settings for environments.
type Options struct {
	BaseDir string
	WorkDir string
	Shell   []string
	Log     io.Writer
}

// New builds the configured environment.
func New(cfg config.Environment, opts Options) (Environment, error) {
	switch cfg.Type {
	case "dry":
		return dry{}, nil
	case "kind":
		return newKind(cfg, opts), nil
	case "exec":
		return &execEnv{cfg: cfg, opts: opts}, nil
	}
	return nil, fmt.Errorf("unknown environment type %q", cfg.Type)
}

// dry renders states to disk only; checks inspect the files (policy
// engines, schema validation, diff tools).
type dry struct{}

func (dry) Name() string                             { return "dry" }
func (dry) Prepare(context.Context) error            { return nil }
func (dry) Apply(context.Context, model.State) error { return nil }
func (dry) Reset(context.Context, model.State) error { return nil }
func (dry) Cleanup(context.Context) error            { return nil }
func (dry) Env() []string                            { return nil }
