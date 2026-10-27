package model

import (
	"strings"
	"testing"
	"time"
)

func at(min int) time.Time { return time.Date(2026, 10, 27, 9, min, 0, 0, time.UTC) }

func TestMergeInterleavesByTimeAndKeepsStreamOrder(t *testing.T) {
	git := Stream{Source: "app", Kind: "git", Baseline: "g0", Changes: []Change{
		{Source: "app", Version: "g1", Time: at(10)},
		{Source: "app", Version: "g2", Time: at(5)}, // out of order: rebased history
		{Source: "app", Version: "g3", Time: at(40)},
	}}
	flags := Stream{Source: "flags", Kind: "exec", Baseline: "f0", Changes: []Change{
		{Source: "flags", Version: "f1", Time: at(1)},
		{Source: "flags", Version: "f2", Time: at(10)},
		{Source: "flags", Version: "f3", Time: at(30)},
	}}
	tl := Merge([]Stream{git, flags})

	var got []string
	for i, c := range tl.Changes {
		if c.Index != i+1 {
			t.Fatalf("change %s has index %d, want %d", c.Version, c.Index, i+1)
		}
		got = append(got, c.Version)
	}
	// g1 and f2 tie at minute 10: the first configured source wins.
	if want := "f1 g1 g2 f2 f3 g3"; strings.Join(got, " ") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
	if tl.Heads["app"] != "g3" || tl.Heads["flags"] != "f3" {
		t.Fatalf("heads = %v", tl.Heads)
	}
}

func TestTimelineAt(t *testing.T) {
	tl := Merge([]Stream{
		{Source: "a", Baseline: "a0", Changes: []Change{{Source: "a", Version: "a1", Time: at(1)}, {Source: "a", Version: "a2", Time: at(3)}}},
		{Source: "b", Baseline: "b0", Changes: []Change{{Source: "b", Version: "b1", Time: at(2)}}},
	})
	cases := map[int]string{0: "a@a0 b@b0", 1: "a@a1 b@b0", 2: "a@a1 b@b1", 3: "a@a2 b@b1"}
	for k, want := range cases {
		if got := tl.At(k).String(); got != want {
			t.Errorf("At(%d) = %s, want %s", k, got, want)
		}
	}
}

func TestStateEnv(t *testing.T) {
	st := State{Index: 2, Total: 5, Dir: "/tmp/x", Snapshot: Snapshot{"my-app.v2": "abc"}}
	env := strings.Join(st.Env(), "\n")
	for _, want := range []string{"INFRABISECT_STATE=2", "INFRABISECT_TOTAL=5", "INFRABISECT_VERSION_MY_APP_V2=abc", `INFRABISECT_SNAPSHOT={"my-app.v2":"abc"}`} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
}
