package source

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// gitSource treats every commit touching Path in a repository as a change.
type gitSource struct {
	cfg  config.Source
	rt   Runtime
	repo string
	bin  string
}

func newGit(cfg config.Source, rt Runtime) *gitSource {
	bin := cfg.Binary
	if bin == "" {
		bin = "git"
	}
	return &gitSource{cfg: cfg, rt: rt, repo: resolve(rt.BaseDir, cfg.Repo), bin: bin}
}

func (g *gitSource) Name() string { return g.cfg.Name }
func (g *gitSource) Kind() string { return "git" }

func (g *gitSource) git(ctx context.Context, args ...string) (string, error) {
	return execx.Output(ctx, g.repo, g.bin, args...)
}

func (g *gitSource) Stream(ctx context.Context, w Window) (model.Stream, error) {
	s := model.Stream{Source: g.cfg.Name, Kind: "git"}
	if _, err := os.Stat(g.repo); err != nil {
		return s, fmt.Errorf("source %s: repo %s: %w", g.cfg.Name, g.repo, err)
	}
	fp := *g.cfg.FirstParent

	badRef := g.cfg.Bad
	if badRef == "" {
		badRef = "HEAD"
	}
	bad, err := g.git(ctx, "rev-parse", "--verify", badRef+"^{commit}")
	if err != nil {
		return s, fmt.Errorf("source %s: resolve bad %q: %w", g.cfg.Name, badRef, err)
	}
	if g.cfg.Bad == "" && !w.Until.IsZero() {
		at, err := g.lastBefore(ctx, bad, w.Until, fp)
		if err != nil {
			return s, err
		}
		if at != "" {
			bad = at
		}
	}

	var good string
	switch {
	case g.cfg.Good != "":
		good, err = g.git(ctx, "rev-parse", "--verify", g.cfg.Good+"^{commit}")
		if err != nil {
			return s, fmt.Errorf("source %s: resolve good %q: %w", g.cfg.Name, g.cfg.Good, err)
		}
	case !w.Since.IsZero():
		good, err = g.lastBefore(ctx, bad, w.Since, fp)
		if err != nil {
			return s, err
		}
		if good == "" {
			return s, fmt.Errorf("source %s: no commit before %s; set good explicitly", g.cfg.Name, w.Since.Format(time.RFC3339))
		}
	default:
		return s, fmt.Errorf("source %s: set good or window.since", g.cfg.Name)
	}
	if _, err := g.git(ctx, "merge-base", "--is-ancestor", good, bad); err != nil {
		return s, fmt.Errorf("source %s: good %s is not an ancestor of bad %s", g.cfg.Name, model.Short(good), model.Short(bad))
	}

	args := []string{"log", "--reverse", "--format=%H%x1f%ct%x1f%an%x1f%s"}
	if fp {
		args = append(args, "--first-parent")
	}
	args = append(args, good+".."+bad)
	if g.cfg.Path != "" {
		args = append(args, "--", filepath.ToSlash(g.cfg.Path))
	}
	out, err := g.git(ctx, args...)
	if err != nil {
		return s, fmt.Errorf("source %s: git log: %w", g.cfg.Name, err)
	}

	s.Baseline, s.Head = good, bad
	prev := good
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 4)
		if len(f) != 4 {
			return s, fmt.Errorf("source %s: unexpected git log line %q", g.cfg.Name, line)
		}
		sec, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return s, fmt.Errorf("source %s: bad commit time %q", g.cfg.Name, f[1])
		}
		s.Changes = append(s.Changes, model.Change{
			Source: g.cfg.Name, Kind: "git", Version: f[0], Prev: prev,
			Time: time.Unix(sec, 0).UTC(), Author: f[2], Summary: f[3],
		})
		prev = f[0]
	}
	return s, nil
}

// lastBefore returns the newest commit reachable from ref committed at or
// before t, or "" when there is none.
func (g *gitSource) lastBefore(ctx context.Context, ref string, t time.Time, firstParent bool) (string, error) {
	args := []string{"rev-list", "-1", "--before=" + strconv.FormatInt(t.Unix(), 10)}
	if firstParent {
		args = append(args, "--first-parent")
	}
	out, err := g.git(ctx, append(args, ref)...)
	if err != nil {
		return "", fmt.Errorf("source %s: rev-list: %w", g.cfg.Name, err)
	}
	return out, nil
}

func (g *gitSource) Render(ctx context.Context, version, outDir string) error {
	tree, err := g.extract(ctx, version)
	if err != nil {
		return err
	}
	dir := tree
	if g.cfg.Path != "" {
		dir = filepath.Join(tree, filepath.FromSlash(g.cfg.Path))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	r := g.cfg.Render
	switch r.Type {
	case "raw":
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return nil // path does not exist at this version: nothing deployed
		}
		return copyTree(dir, outDir)
	case "kustomize":
		res, err := execx.Run(ctx, execx.Cmd{Name: "kubectl", Args: []string{"kustomize", dir}})
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(outDir, "manifests.yaml"), res.Stdout, 0o644)
	case "helm":
		chart := dir
		if r.Chart != "" {
			chart = filepath.Join(tree, filepath.FromSlash(r.Chart))
		}
		name := r.Release
		if name == "" {
			name = g.cfg.Name
		}
		args := []string{"template", name, chart}
		if r.Namespace != "" {
			args = append(args, "--namespace", r.Namespace)
		}
		for _, v := range r.Values {
			args = append(args, "-f", filepath.Join(tree, filepath.FromSlash(v)))
		}
		res, err := execx.Run(ctx, execx.Cmd{Name: "helm", Args: args})
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(outDir, "manifests.yaml"), res.Stdout, 0o644)
	case "command":
		_, err := execx.Shell(ctx, g.rt.Shell, execx.Cmd{
			Dir:    tree,
			Stdout: g.rt.Log, Stderr: g.rt.Log,
			Env: []string{
				"INFRABISECT_SOURCE=" + g.cfg.Name,
				"INFRABISECT_VERSION=" + version,
				"INFRABISECT_SRC_DIR=" + dir,
				"INFRABISECT_OUT_DIR=" + outDir,
			},
		}, r.Command)
		return err
	}
	return fmt.Errorf("source %s: unknown render type %q", g.cfg.Name, r.Type)
}

// extract materializes the full tree at sha into a cache directory once.
func (g *gitSource) extract(ctx context.Context, sha string) (string, error) {
	dir := filepath.Join(g.rt.CacheDir, "git", g.cfg.Name, sha)
	marker := filepath.Join(dir, ".infrabisect-complete")
	if _, err := os.Stat(marker); err == nil {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	zipPath := dir + ".zip"
	defer os.Remove(zipPath)
	if _, err := execx.Run(ctx, execx.Cmd{Name: g.bin, Dir: g.repo, Args: []string{"archive", "--format=zip", "-o", zipPath, sha}}); err != nil {
		return "", fmt.Errorf("source %s: git archive %s: %w", g.cfg.Name, model.Short(sha), err)
	}
	if err := unzip(zipPath, dir); err != nil {
		return "", fmt.Errorf("source %s: extract %s: %w", g.cfg.Name, model.Short(sha), err)
	}
	return dir, os.WriteFile(marker, nil, 0o644)
}

func unzip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target := filepath.Join(root, filepath.FromSlash(f.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("illegal path in archive: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue // symlinks are skipped; manifests should not depend on them
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm()|0o200)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *gitSource) Remedy(c model.Change) model.Remedy {
	return model.Remedy{
		Description: fmt.Sprintf("Revert commit %s (%q) in %s", c.ShortVersion(), c.Summary, g.cfg.Repo),
		Commands:    []string{fmt.Sprintf("git -C %s revert --no-edit %s", quote(g.repo), c.Version)},
	}
}

// Revert creates branch infrabisect/revert-<sha> in a separate worktree,
// based on the bad version, with the culprit reverted. The user's own
// checkout is never modified.
func (g *gitSource) Revert(ctx context.Context, c model.Change, bad string) (string, error) {
	branch := "infrabisect/revert-" + c.ShortVersion()
	wt := filepath.Join(g.rt.CacheDir, "worktrees", branch)
	_ = os.RemoveAll(wt)
	if _, err := g.git(ctx, "worktree", "add", "-B", branch, wt, bad); err != nil {
		return "", fmt.Errorf("create worktree: %w", err)
	}
	if _, err := execx.Output(ctx, wt, g.bin, "revert", "--no-edit", c.Version); err != nil {
		_, _ = execx.Output(ctx, wt, g.bin, "revert", "--abort")
		return "", fmt.Errorf("revert %s does not apply cleanly: %w", c.ShortVersion(), err)
	}
	return fmt.Sprintf("branch %s (worktree %s)\n  next: git -C %s push -u origin %s && gh pr create --head %s --fill",
		branch, wt, quote(g.repo), branch, branch), nil
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return `"` + s + `"`
	}
	return s
}
