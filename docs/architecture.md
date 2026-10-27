# Architecture

```
            ┌───────────── sources ─────────────┐
 git repo ──┤ git:  commits touching a path      │
 helm     ──┤ helm: release revisions            ├──► Stream per source
 anything ──┤ exec: list + render commands       │     (baseline + changes)
            └────────────────────────────────────┘
                              │ k-way merge by time
                              ▼
                 Timeline: c1 c2 c3 … cN   (state k = baseline + c1..ck)
                              │
                              ▼  binary search with skips (bisect)
        ┌──────────── for each probed state k ────────────┐
        │ Materialize: render every source at its version │
        │ Environment.Apply  (dry | kind | exec)          │
        │ Checks             (command | http | rollout)   │
        │ Environment.Reset                               │
        └──────────────────────────────────────────────────┘
                              │
                              ▼
                  Report: culprit, remedy, steps, timeline
                  (terminal, Markdown, JSON; optional revert branch)
```

## Packages

| Package | Responsibility |
|---|---|
| `internal/model` | Domain types: `Change`, `Stream`, `Timeline` (merge, `At(k)`), `Snapshot`, `State`, `Verdict`, `Remedy` |
| `internal/config` | YAML loading, defaults, validation that reports every error at once, time parsing |
| `internal/source` | `Source` interface and the `git`, `helm` and `exec` implementations; optional `Reverter` |
| `internal/bisect` | Pure search algorithm over states 0..N with skip handling and endpoint verification |
| `internal/env` | `Environment` interface: `dry`, `kind`, `exec` |
| `internal/check` | `Checker` interface: `command`, `http`, `rollout`; retries |
| `internal/runner` | Wires everything together, owns the work directory and `run.log` |
| `internal/report` | Text, Markdown and JSON renderers, exit codes |
| `internal/cli` | Commands and flags (standard library `flag`, no framework) |

## Design decisions

**Snapshots, not patches.** A state is defined as "every source at version X", never as a diff applied on top of something else. Rendering a snapshot is deterministic and cacheable. Sources don't need to know about each other, and a culprit always maps to exactly one change.

**Merge keeps each source's order.** Commit timestamps lie after rebases and cherry-picks. The k-way merge only compares the heads of the streams, so a source's own history is never reordered, and the merged timeline is still a valid sequence of real states for each source.

**Your working copy is never touched.** Git versions are extracted with `git archive` into a cache. Reverts happen in a separate worktree and are never pushed.

**Untestable is a first-class result.** As in `git bisect run`, exit code 125 skips a state. The search then probes the state nearest the middle instead. If skips hide the boundary, InfraBisect reports the remaining candidates instead of guessing.

**One external dependency.** Only `gopkg.in/yaml.v3`. All integrations shell out to the tools operators already have (`git`, `kubectl`, `helm`, `kind`), so InfraBisect inherits their authentication and works with any version of them.

## Adding a source

Implement `source.Source`:

```go
type Source interface {
    Name() string
    Kind() string
    Stream(ctx context.Context, w Window) (model.Stream, error) // baseline + ordered changes
    Render(ctx context.Context, version, outDir string) error    // write that version to disk
    Remedy(c model.Change) model.Remedy                          // how to undo it
}
```

Register it in `source.New`, add validation in `config.Validate`, and add a test built on recorded tool output (see `buildHelmStream`). Argo CD, Terraform state versions, container registries and cloud audit logs are all good candidates.
