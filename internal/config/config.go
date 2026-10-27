// Package config loads and validates infrabisect.yaml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultFile is the configuration file looked up when none is given.
const DefaultFile = "infrabisect.yaml"

// Config is the root of infrabisect.yaml.
type Config struct {
	Version     int         `yaml:"version"`
	WorkDir     string      `yaml:"workDir"`
	Shell       []string    `yaml:"shell"`
	Window      Window      `yaml:"window"`
	Sources     []Source    `yaml:"sources"`
	Environment Environment `yaml:"environment"`
	Checks      []Check     `yaml:"checks"`
	Bisect      Bisect      `yaml:"bisect"`

	// BaseDir is the directory of the config file; relative paths and
	// commands resolve against it.
	BaseDir string `yaml:"-"`
}

// Window bounds the incident: changes after Since and up to Until.
type Window struct {
	Since string `yaml:"since"`
	Until string `yaml:"until"`
}

// Source is one stream of infrastructure changes.
type Source struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // git | helm | exec

	// Good is the known-good version (git ref, Helm revision, exec version).
	// Bad is the broken version; it defaults to the latest one.
	Good string `yaml:"good"`
	Bad  string `yaml:"bad"`

	// git
	Repo        string `yaml:"repo"`
	Path        string `yaml:"path"`
	FirstParent *bool  `yaml:"firstParent"`

	// helm
	Release     string `yaml:"release"`
	Namespace   string `yaml:"namespace"`
	KubeContext string `yaml:"kubeContext"`

	// exec
	List   string `yaml:"list"`
	Remedy string `yaml:"remedy"`

	Render Render `yaml:"render"`
	Binary string `yaml:"binary"` // override for git/helm executables
}

// Render controls how a source version becomes files on disk.
type Render struct {
	Type      string   `yaml:"type"` // raw | kustomize | helm | command
	Command   string   `yaml:"command"`
	Chart     string   `yaml:"chart"`
	Release   string   `yaml:"release"`
	Namespace string   `yaml:"namespace"`
	Values    []string `yaml:"values"`
}

// Environment is where each candidate state is reproduced.
type Environment struct {
	Type string `yaml:"type"` // dry | kind | exec

	// kind
	Cluster      string   `yaml:"cluster"`
	NodeImage    string   `yaml:"nodeImage"`
	Reuse        bool     `yaml:"reuse"`
	Fresh        bool     `yaml:"fresh"`
	Keep         bool     `yaml:"keep"`
	LoadImages   []string `yaml:"loadImages"`
	ApplySources []string `yaml:"applySources"`
	Timeout      Duration `yaml:"timeout"`

	// exec
	Setup    string `yaml:"setup"`
	Apply    string `yaml:"apply"`
	Reset    string `yaml:"reset"`
	Teardown string `yaml:"teardown"`
}

// Check decides whether a state is good, bad or untestable.
type Check struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // command | http | rollout

	Run          string   `yaml:"run"`
	URL          string   `yaml:"url"`
	ExpectStatus int      `yaml:"expectStatus"`
	BodyContains string   `yaml:"bodyContains"`
	Namespaces   []string `yaml:"namespaces"`

	Timeout  Duration `yaml:"timeout"`
	Retries  int      `yaml:"retries"`
	Interval Duration `yaml:"interval"`
}

// Bisect tunes the search.
type Bisect struct {
	VerifyGood    bool   `yaml:"verifyGood"`
	VerifyBad     *bool  `yaml:"verifyBad"`
	OnRenderError string `yaml:"onRenderError"` // bad | skip
	OnApplyError  string `yaml:"onApplyError"`  // bad | skip
	KeepStates    bool   `yaml:"keepStates"`
}

// Duration is a time.Duration that unmarshals from strings like "90s".
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	d.Duration = v
	return nil
}

// Or returns the duration or a fallback when unset.
func (d Duration) Or(fallback time.Duration) time.Duration {
	if d.Duration <= 0 {
		return fallback
	}
	return d.Duration
}

// Load reads, expands ${ENV} references in, defaults and validates a file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(raw, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes configuration bytes; baseDir anchors relative paths.
func Parse(raw []byte, baseDir string) (*Config, error) {
	// Only ${VAR} references to variables that are set are expanded, so
	// shell snippets ($1, $INFRABISECT_STATE_DIR, ...) pass through intact.
	expanded := envRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		if v, ok := os.LookupEnv(string(m[2 : len(m)-1])); ok {
			return []byte(v)
		}
		return m
	})
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	cfg.BaseDir = baseDir
	cfg.applyDefaults()
	return &cfg, cfg.Validate()
}

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.WorkDir == "" {
		c.WorkDir = ".infrabisect"
	}
	if c.Environment.Type == "" {
		c.Environment.Type = "dry"
	}
	if c.Environment.Cluster == "" {
		c.Environment.Cluster = "infrabisect"
	}
	if c.Bisect.VerifyBad == nil {
		t := true
		c.Bisect.VerifyBad = &t
	}
	if c.Bisect.OnRenderError == "" {
		c.Bisect.OnRenderError = "bad"
	}
	if c.Bisect.OnApplyError == "" {
		c.Bisect.OnApplyError = "bad"
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Type == "git" && s.Render.Type == "" {
			s.Render.Type = "raw"
		}
		if s.Type == "exec" && s.Render.Type == "" {
			s.Render.Type = "command"
		}
		if s.FirstParent == nil {
			t := true
			s.FirstParent = &t
		}
	}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.Name == "" {
			ch.Name = fmt.Sprintf("%s-%d", ch.Type, i+1)
		}
		if ch.Type == "http" && ch.ExpectStatus == 0 {
			ch.ExpectStatus = 200
		}
	}
}

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	envRe  = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)
)

// Validate reports every configuration problem at once.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Version != 1 {
		add("version: unsupported version %d (expected 1)", c.Version)
	}
	if len(c.Sources) == 0 {
		add("sources: at least one source is required")
	}
	seen := map[string]bool{}
	for i, s := range c.Sources {
		where := fmt.Sprintf("sources[%d]", i)
		if s.Name != "" {
			where = fmt.Sprintf("sources[%s]", s.Name)
		}
		if !nameRe.MatchString(s.Name) {
			add("%s: name %q must match %s", where, s.Name, nameRe)
		}
		if seen[s.Name] {
			add("%s: duplicate source name", where)
		}
		seen[s.Name] = true
		switch s.Type {
		case "git":
			if s.Repo == "" {
				add("%s: repo is required for git sources", where)
			}
			switch s.Render.Type {
			case "raw", "kustomize":
			case "helm":
				if s.Render.Chart == "" && s.Path == "" {
					add("%s: render.chart or path is required for helm rendering", where)
				}
			case "command":
				if s.Render.Command == "" {
					add("%s: render.command is required", where)
				}
			default:
				add("%s: unknown render.type %q (raw, kustomize, helm, command)", where, s.Render.Type)
			}
		case "helm":
			if s.Release == "" {
				add("%s: release is required for helm sources", where)
			}
		case "exec":
			if s.List == "" {
				add("%s: list is required for exec sources", where)
			}
			if s.Render.Command == "" {
				add("%s: render.command is required for exec sources", where)
			}
		default:
			add("%s: unknown type %q (git, helm, exec)", where, s.Type)
		}
	}

	e := c.Environment
	switch e.Type {
	case "dry":
	case "kind":
		for _, name := range e.ApplySources {
			if !seen[name] {
				add("environment.applySources: unknown source %q", name)
			}
		}
	case "exec":
		if e.Apply == "" {
			add("environment.apply is required for exec environments")
		}
	default:
		add("environment.type: unknown type %q (dry, kind, exec)", e.Type)
	}

	if len(c.Checks) == 0 {
		add("checks: at least one check is required")
	}
	for i, ch := range c.Checks {
		where := fmt.Sprintf("checks[%d]", i)
		switch ch.Type {
		case "command":
			if ch.Run == "" {
				add("%s: run is required for command checks", where)
			}
		case "http":
			if ch.URL == "" {
				add("%s: url is required for http checks", where)
			}
		case "rollout":
		default:
			add("%s: unknown type %q (command, http, rollout)", where, ch.Type)
		}
		if ch.Retries < 0 {
			add("%s: retries must be >= 0", where)
		}
	}

	for _, v := range []struct{ field, val string }{
		{"bisect.onRenderError", c.Bisect.OnRenderError},
		{"bisect.onApplyError", c.Bisect.OnApplyError},
	} {
		if v.val != "bad" && v.val != "skip" {
			add("%s: must be bad or skip, got %q", v.field, v.val)
		}
	}
	if _, err := ParseTime(c.Window.Since, time.Now()); err != nil {
		add("window.since: %v", err)
	}
	if _, err := ParseTime(c.Window.Until, time.Now()); err != nil {
		add("window.until: %v", err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Resolve makes a path relative to the config file absolute.
func (c *Config) Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.BaseDir, p)
}

// ParseTime accepts RFC 3339, "2006-01-02 15:04[:05]", "2006-01-02" or a
// duration such as "6h" meaning that long before now. Empty yields zero.
func ParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q (use RFC 3339, \"2006-01-02 15:04\" or a duration like 6h)", s)
}
