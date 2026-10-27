package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pughalan/infrabisect/internal/config"
	"github.com/pughalan/infrabisect/internal/execx"
	"github.com/pughalan/infrabisect/internal/model"
)

// kindEnv replays each state into a throwaway Kubernetes-in-Docker
// cluster: apply the rendered manifests, run checks, delete them again.
type kindEnv struct {
	cfg        config.Environment
	opts       Options
	kubeconfig string
	created    bool
	applied    int
}

func newKind(cfg config.Environment, opts Options) *kindEnv {
	return &kindEnv{cfg: cfg, opts: opts, kubeconfig: filepath.Join(opts.WorkDir, "kubeconfig")}
}

func (k *kindEnv) Name() string { return "kind:" + k.cfg.Cluster }

func (k *kindEnv) Env() []string { return []string{"KUBECONFIG=" + k.kubeconfig} }

func (k *kindEnv) run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Run(ctx, execx.Cmd{Name: name, Args: args, Dir: k.opts.BaseDir, Stdout: k.opts.Log, Stderr: k.opts.Log})
}

func (k *kindEnv) kubectl(ctx context.Context, args ...string) (execx.Result, error) {
	return k.run(ctx, "kubectl", append([]string{"--kubeconfig", k.kubeconfig}, args...)...)
}

func (k *kindEnv) exists(ctx context.Context) (bool, error) {
	out, err := execx.Output(ctx, k.opts.BaseDir, "kind", "get", "clusters")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == k.cfg.Cluster {
			return true, nil
		}
	}
	return false, nil
}

func (k *kindEnv) Prepare(ctx context.Context) error {
	if err := os.MkdirAll(k.opts.WorkDir, 0o755); err != nil {
		return err
	}
	ok, err := k.exists(ctx)
	if err != nil {
		return fmt.Errorf("kind: %w", err)
	}
	switch {
	case ok && k.cfg.Reuse:
		res, err := k.run(ctx, "kind", "get", "kubeconfig", "--name", k.cfg.Cluster)
		if err != nil {
			return err
		}
		if err := os.WriteFile(k.kubeconfig, res.Stdout, 0o600); err != nil {
			return err
		}
	case ok:
		return fmt.Errorf("kind cluster %q already exists; set environment.reuse: true or pick another environment.cluster", k.cfg.Cluster)
	default:
		if err := k.create(ctx); err != nil {
			return err
		}
	}
	for _, img := range k.cfg.LoadImages {
		if err := k.loadImage(ctx, img); err != nil {
			return fmt.Errorf("load image %s: %w", img, err)
		}
	}
	return nil
}

// loadImage side-loads a host image into the cluster. `kind load
// docker-image` fails for multi-platform images under Docker Desktop's
// containerd image store ("content digest ... not found"), so on failure the
// image is exported for the node's platform only and loaded as an archive.
func (k *kindEnv) loadImage(ctx context.Context, img string) error {
	_, err := k.run(ctx, "kind", "load", "docker-image", img, "--name", k.cfg.Cluster)
	if err == nil {
		return nil
	}
	platform, perr := execx.Output(ctx, k.opts.BaseDir, "docker", "exec", k.cfg.Cluster+"-control-plane",
		"sh", "-c", `echo "linux/$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)"`)
	if perr != nil {
		return err
	}
	archive := filepath.Join(k.opts.WorkDir, "image.tar")
	defer os.Remove(archive)
	if _, serr := k.run(ctx, "docker", "save", "--platform", platform, "-o", archive, img); serr != nil {
		return fmt.Errorf("%w; fallback docker save --platform %s also failed: %v", err, platform, serr)
	}
	if _, lerr := k.run(ctx, "kind", "load", "image-archive", archive, "--name", k.cfg.Cluster); lerr != nil {
		return fmt.Errorf("%w; fallback image-archive load also failed: %v", err, lerr)
	}
	return nil
}

func (k *kindEnv) create(ctx context.Context) error {
	args := []string{"create", "cluster", "--name", k.cfg.Cluster, "--kubeconfig", k.kubeconfig, "--wait", "180s"}
	if k.cfg.NodeImage != "" {
		args = append(args, "--image", k.cfg.NodeImage)
	}
	if _, err := k.run(ctx, "kind", args...); err != nil {
		return fmt.Errorf("create kind cluster: %w", err)
	}
	k.created = true
	return nil
}

// dirs returns the rendered source directories to apply.
func (k *kindEnv) dirs(st model.State) []string {
	names := k.cfg.ApplySources
	if len(names) == 0 {
		for n := range st.Snapshot {
			names = append(names, n)
		}
	}
	var dirs []string
	for _, n := range names {
		d := filepath.Join(st.Dir, n)
		if hasManifests(d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func (k *kindEnv) Apply(ctx context.Context, st model.State) error {
	// fresh: every state after the first gets a brand-new cluster, so no
	// cluster-scoped leftovers (CRDs, webhooks, PVs) leak between states.
	if k.cfg.Fresh && k.created && k.applied > 0 {
		if _, err := k.run(ctx, "kind", "delete", "cluster", "--name", k.cfg.Cluster); err != nil {
			return err
		}
		if err := k.create(ctx); err != nil {
			return err
		}
	}
	k.applied++
	dirs := k.dirs(st)
	if len(dirs) == 0 {
		return nil
	}
	args := []string{"apply", "--recursive"}
	for _, d := range dirs {
		args = append(args, "-f", d)
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.Timeout.Or(5*time.Minute))
	defer cancel()
	// Namespaces and CRDs may be declared in later files than the objects
	// that use them; a second pass resolves that ordering.
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err = k.kubectl(ctx, args...); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

func (k *kindEnv) Reset(ctx context.Context, st model.State) error {
	if k.cfg.Fresh {
		return nil
	}
	dirs := k.dirs(st)
	if len(dirs) == 0 {
		return nil
	}
	args := []string{"delete", "--recursive", "--ignore-not-found", "--wait=true", "--timeout=180s"}
	for _, d := range dirs {
		args = append(args, "-f", d)
	}
	_, err := k.kubectl(ctx, args...)
	return err
}

func (k *kindEnv) Cleanup(ctx context.Context) error {
	if !k.created || k.cfg.Keep {
		return nil
	}
	_, err := k.run(ctx, "kind", "delete", "cluster", "--name", k.cfg.Cluster)
	return err
}

func hasManifests(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml", ".json":
			if !fi.IsDir() {
				found = true
			}
		}
		return nil
	})
	return found
}
