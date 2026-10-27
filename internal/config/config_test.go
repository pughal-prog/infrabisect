package config

import (
	"strings"
	"testing"
	"time"
)

const valid = `
version: 1
sources:
  - name: app
    type: git
    repo: ./repo
    good: v1
  - name: flags
    type: exec
    list: ./list.sh
    render:
      command: ./render.sh '$1' ${INFRABISECT_TEST_VALUE} ${NOT_SET_ANYWHERE}
environment:
  type: kind
  applySources: [app]
  timeout: 90s
checks:
  - type: rollout
  - type: http
    url: http://localhost:8080/health
`

func TestParseValid(t *testing.T) {
	t.Setenv("INFRABISECT_TEST_VALUE", "expanded")
	cfg, err := Parse([]byte(valid), "/base")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != ".infrabisect" || cfg.Environment.Cluster != "infrabisect" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.Sources[0].Render.Type != "raw" || !*cfg.Sources[0].FirstParent {
		t.Errorf("git defaults not applied: %+v", cfg.Sources[0])
	}
	if got := cfg.Sources[1].Render.Command; got != "./render.sh '$1' expanded ${NOT_SET_ANYWHERE}" {
		t.Errorf("render command = %q", got)
	}
	if cfg.Environment.Timeout.Duration != 90*time.Second {
		t.Errorf("timeout = %v", cfg.Environment.Timeout)
	}
	if cfg.Checks[1].ExpectStatus != 200 || cfg.Checks[0].Name != "rollout-1" {
		t.Errorf("check defaults not applied: %+v", cfg.Checks)
	}
	if !*cfg.Bisect.VerifyBad || cfg.Bisect.OnApplyError != "bad" {
		t.Errorf("bisect defaults not applied: %+v", cfg.Bisect)
	}
}

func TestParseReportsAllErrors(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
sources:
  - name: "bad name"
    type: git
  - name: x
    type: svn
environment:
  type: kind
  applySources: [ghost]
checks:
  - type: command
`), "/")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"must match", "repo is required", `unknown type "svn"`, `unknown source "ghost"`, "run is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("version: 1\nsourcez: []\n"), "/")
	if err == nil || !strings.Contains(err.Error(), "sourcez") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	got, err := ParseTime("6h", now)
	if err != nil || !got.Equal(now.Add(-6*time.Hour)) {
		t.Errorf("6h: %v %v", got, err)
	}
	got, err = ParseTime("2026-10-27T09:30:00Z", now)
	if err != nil || got.Hour() != 9 || got.Minute() != 30 {
		t.Errorf("rfc3339: %v %v", got, err)
	}
	if _, err := ParseTime("2026-10-27 09:30", now); err != nil {
		t.Error(err)
	}
	if got, err := ParseTime("", now); err != nil || !got.IsZero() {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err := ParseTime("yesterday-ish", now); err == nil {
		t.Error("expected error")
	}
}
