package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pughal-prog/infrabisect/internal/config"
	"github.com/pughal-prog/infrabisect/internal/execx"
	"github.com/pughal-prog/infrabisect/internal/model"
)

// helmSource treats every revision of a Helm release as a change and
// renders a revision from the manifest Helm stored for it.
type helmSource struct {
	cfg config.Source
	rt  Runtime
	bin string
}

func newHelm(cfg config.Source, rt Runtime) *helmSource {
	bin := cfg.Binary
	if bin == "" {
		bin = "helm"
	}
	return &helmSource{cfg: cfg, rt: rt, bin: bin}
}

func (h *helmSource) Name() string { return h.cfg.Name }
func (h *helmSource) Kind() string { return "helm" }

func (h *helmSource) scope() []string {
	var a []string
	if h.cfg.Namespace != "" {
		a = append(a, "--namespace", h.cfg.Namespace)
	}
	if h.cfg.KubeContext != "" {
		a = append(a, "--kube-context", h.cfg.KubeContext)
	}
	return a
}

// helmRevision mirrors one entry of `helm history -o json`.
type helmRevision struct {
	Revision    int    `json:"revision"`
	Updated     string `json:"updated"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"app_version"`
	Description string `json:"description"`
}

func (h *helmSource) Stream(ctx context.Context, w Window) (model.Stream, error) {
	s := model.Stream{Source: h.cfg.Name, Kind: "helm"}
	args := append([]string{"history", h.cfg.Release, "--output", "json", "--max", "1000"}, h.scope()...)
	out, err := execx.Output(ctx, h.rt.BaseDir, h.bin, args...)
	if err != nil {
		return s, fmt.Errorf("source %s: helm history: %w", h.cfg.Name, err)
	}
	return buildHelmStream(h.cfg, []byte(out), w)
}

// buildHelmStream turns `helm history` JSON into a stream. It is separate
// from Stream so it can be tested without a cluster.
func buildHelmStream(cfg config.Source, raw []byte, w Window) (model.Stream, error) {
	s := model.Stream{Source: cfg.Name, Kind: "helm"}
	var revs []helmRevision
	if err := json.Unmarshal(raw, &revs); err != nil {
		return s, fmt.Errorf("source %s: parse helm history: %w", cfg.Name, err)
	}
	if len(revs) == 0 {
		return s, fmt.Errorf("source %s: release %s has no history", cfg.Name, cfg.Release)
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].Revision < revs[j].Revision })
	type rev struct {
		helmRevision
		t model.Change
	}
	parsed := make([]rev, len(revs))
	for i, r := range revs {
		t, err := parseTime(r.Updated)
		if err != nil {
			return s, fmt.Errorf("source %s: revision %d: %w", cfg.Name, r.Revision, err)
		}
		summary := r.Chart
		if r.AppVersion != "" {
			summary += " (app " + r.AppVersion + ")"
		}
		if d := strings.TrimSpace(r.Description); d != "" {
			summary += " - " + d
		}
		parsed[i] = rev{r, model.Change{
			Source: cfg.Name, Kind: "helm", Version: strconv.Itoa(r.Revision),
			Time: t.UTC(), Summary: summary,
		}}
	}

	bad := parsed[len(parsed)-1].Revision
	if cfg.Bad != "" {
		n, err := strconv.Atoi(cfg.Bad)
		if err != nil {
			return s, fmt.Errorf("source %s: bad must be a revision number, got %q", cfg.Name, cfg.Bad)
		}
		bad = n
	} else if !w.Until.IsZero() {
		for i := len(parsed) - 1; i >= 0; i-- {
			if !parsed[i].t.Time.After(w.Until) {
				bad = parsed[i].Revision
				break
			}
		}
	}

	good := -1
	switch {
	case cfg.Good != "":
		n, err := strconv.Atoi(cfg.Good)
		if err != nil {
			return s, fmt.Errorf("source %s: good must be a revision number, got %q", cfg.Name, cfg.Good)
		}
		good = n
	case !w.Since.IsZero():
		for _, r := range parsed {
			if r.t.Time.After(w.Since) {
				break
			}
			good = r.Revision
		}
		if good < 0 {
			return s, fmt.Errorf("source %s: no revision before window.since; set good explicitly", cfg.Name)
		}
	default:
		return s, fmt.Errorf("source %s: set good or window.since", cfg.Name)
	}
	if good > bad {
		return s, fmt.Errorf("source %s: good revision %d is after bad revision %d", cfg.Name, good, bad)
	}

	s.Baseline, s.Head = strconv.Itoa(good), strconv.Itoa(bad)
	prev := s.Baseline
	for _, r := range parsed {
		if r.Revision <= good || r.Revision > bad {
			continue
		}
		c := r.t
		c.Prev = prev
		prev = c.Version
		s.Changes = append(s.Changes, c)
	}
	return s, nil
}

func (h *helmSource) Render(ctx context.Context, version, outDir string) error {
	args := append([]string{"get", "manifest", h.cfg.Release, "--revision", version}, h.scope()...)
	res, err := execx.Run(ctx, execx.Cmd{Name: h.bin, Args: args, Dir: h.rt.BaseDir})
	if err != nil {
		return fmt.Errorf("source %s: helm get manifest: %w", h.cfg.Name, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "manifest.yaml"), res.Stdout, 0o644)
}

func (h *helmSource) Remedy(c model.Change) model.Remedy {
	cmd := fmt.Sprintf("helm rollback %s %s", h.cfg.Release, c.Prev)
	if s := strings.Join(h.scope(), " "); s != "" {
		cmd += " " + s
	}
	return model.Remedy{
		Description: fmt.Sprintf("Roll release %s back from revision %s to %s", h.cfg.Release, c.Version, c.Prev),
		Commands:    []string{cmd},
	}
}
