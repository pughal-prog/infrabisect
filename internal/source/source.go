// Package source discovers changes from infrastructure systems and renders
// any version of them to disk.
package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// Window bounds the incident. Zero times are unbounded.
type Window struct {
	Since time.Time
	Until time.Time
}

// Source is one stream of infrastructure changes.
type Source interface {
	Name() string
	Kind() string
	// Stream returns the known-good baseline and the ordered changes after it.
	Stream(ctx context.Context, w Window) (model.Stream, error)
	// Render writes the desired state at version into outDir.
	Render(ctx context.Context, version, outDir string) error
	// Remedy describes how to undo a change from this source.
	Remedy(c model.Change) model.Remedy
}

// Reverter is implemented by sources that can prepare a fix automatically.
type Reverter interface {
	// Revert prepares the undo of c without touching the user's checkout and
	// returns a human-readable description of what was created.
	Revert(ctx context.Context, c model.Change, bad string) (string, error)
}

// Runtime carries settings shared by all sources.
type Runtime struct {
	BaseDir  string
	CacheDir string
	Shell    []string
	Log      io.Writer
}

// New builds a source from configuration.
func New(cfg config.Source, rt Runtime) (Source, error) {
	switch cfg.Type {
	case "git":
		return newGit(cfg, rt), nil
	case "helm":
		return newHelm(cfg, rt), nil
	case "exec":
		return newExec(cfg, rt), nil
	}
	return nil, fmt.Errorf("source %s: unknown type %q", cfg.Name, cfg.Type)
}

func resolve(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// copyTree copies the contents of src into dst.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, filepath.Join(dst, filepath.Base(src)), info.Mode())
	}
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			if fi.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target, fi.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// parseTime accepts the timestamp formats emitted by the tools we read.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q", s)
}
