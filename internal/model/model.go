// Package model holds the domain types shared across InfraBisect: changes,
// per-source streams, the merged timeline, snapshots and verdicts.
package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Change is a single, atomic change to one source (a commit, a Helm
// revision, a flag flip, ...).
type Change struct {
	Index   int       `json:"index"` // 1-based position in the merged timeline
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Version string    `json:"version"`
	Prev    string    `json:"prev"` // version of the same source before this change
	Time    time.Time `json:"time"`
	Author  string    `json:"author,omitempty"`
	Summary string    `json:"summary"`
}

// ShortVersion abbreviates long versions such as git SHAs.
func (c Change) ShortVersion() string { return Short(c.Version) }

// Ref renders the change as "source@version".
func (c Change) Ref() string { return c.Source + "@" + c.ShortVersion() }

// Short abbreviates a version string to at most 12 characters.
func Short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

// Stream is the ordered list of changes for one source after its baseline.
type Stream struct {
	Source   string
	Kind     string
	Baseline string
	Head     string
	Changes  []Change
}

// Snapshot maps every source name to the version it is at in a given state.
type Snapshot map[string]string

// String renders the snapshot deterministically.
func (s Snapshot) String() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "@" + Short(s[k])
	}
	return strings.Join(parts, " ")
}

// JSON renders the snapshot as a JSON object.
func (s Snapshot) JSON() string {
	b, _ := json.Marshal(map[string]string(s))
	return string(b)
}

// Timeline is the merged, causally ordered list of changes across sources.
// State k is the baseline with changes 1..k applied; state 0 is the
// known-good baseline and state Len() is the broken present.
type Timeline struct {
	Sources  []string
	Kinds    map[string]string
	Baseline Snapshot
	Heads    Snapshot
	Changes  []Change
}

// Len returns the number of changes.
func (t *Timeline) Len() int { return len(t.Changes) }

// At returns the snapshot of state k.
func (t *Timeline) At(k int) Snapshot {
	if k < 0 || k > len(t.Changes) {
		panic(fmt.Sprintf("state %d out of range [0,%d]", k, len(t.Changes)))
	}
	s := make(Snapshot, len(t.Baseline))
	for name, v := range t.Baseline {
		s[name] = v
	}
	for _, c := range t.Changes[:k] {
		s[c.Source] = c.Version
	}
	return s
}

// CountBySource returns how many changes each source contributes.
func (t *Timeline) CountBySource() map[string]int {
	m := make(map[string]int, len(t.Sources))
	for _, c := range t.Changes {
		m[c.Source]++
	}
	return m
}

// Merge interleaves per-source streams by time with a k-way merge. Each
// stream's internal order is preserved even when its timestamps are not
// monotonic (for example rebased git history); ties go to the stream listed
// first in the configuration.
func Merge(streams []Stream) *Timeline {
	t := &Timeline{
		Kinds:    make(map[string]string, len(streams)),
		Baseline: make(Snapshot, len(streams)),
		Heads:    make(Snapshot, len(streams)),
	}
	heads := make([]int, len(streams))
	total := 0
	for _, s := range streams {
		t.Sources = append(t.Sources, s.Source)
		t.Kinds[s.Source] = s.Kind
		t.Baseline[s.Source] = s.Baseline
		head := s.Baseline
		if n := len(s.Changes); n > 0 {
			head = s.Changes[n-1].Version
		}
		t.Heads[s.Source] = head
		total += len(s.Changes)
	}
	for len(t.Changes) < total {
		best := -1
		for i, s := range streams {
			if heads[i] >= len(s.Changes) {
				continue
			}
			if best < 0 || s.Changes[heads[i]].Time.Before(streams[best].Changes[heads[best]].Time) {
				best = i
			}
		}
		c := streams[best].Changes[heads[best]]
		heads[best]++
		c.Index = len(t.Changes) + 1
		t.Changes = append(t.Changes, c)
	}
	return t
}

// Verdict is the outcome of testing one state.
type Verdict int

const (
	Good Verdict = iota
	Bad
	Skip
)

func (v Verdict) String() string {
	switch v {
	case Good:
		return "good"
	case Bad:
		return "bad"
	default:
		return "skip"
	}
}

// MarshalJSON encodes the verdict as its name.
func (v Verdict) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }

// State is a materialized point on the timeline that is being tested.
type State struct {
	Index    int
	Total    int
	Snapshot Snapshot
	Dir      string // root of rendered sources, one subdirectory per source
}

// Env returns the variables every hook and check receives for this state.
func (s State) Env() []string {
	env := []string{
		"INFRABISECT_STATE=" + fmt.Sprint(s.Index),
		"INFRABISECT_TOTAL=" + fmt.Sprint(s.Total),
		"INFRABISECT_STATE_DIR=" + s.Dir,
		"INFRABISECT_SNAPSHOT=" + s.Snapshot.JSON(),
	}
	for name, v := range s.Snapshot {
		env = append(env, "INFRABISECT_VERSION_"+EnvName(name)+"="+v)
	}
	return env
}

// EnvName converts a source name into an environment-variable suffix.
func EnvName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
}

// Remedy describes how to undo a culprit change.
type Remedy struct {
	Description string   `json:"description"`
	Commands    []string `json:"commands,omitempty"`
}
