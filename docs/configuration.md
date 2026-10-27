# Configuration reference

InfraBisect reads `infrabisect.yaml` from the current directory (override with `-c`). Relative paths and commands resolve against the directory of the config file. `${VAR}` references to environment variables that are set are expanded when the file loads. Every other `$` passes through untouched, so shell snippets keep working.

```yaml
version: 1               # required, always 1
workDir: .infrabisect    # cache, rendered states, kubeconfig, run.log
shell: [sh, -c]          # optional; default: sh (Git Bash on Windows), else cmd /C

window:                  # the incident window, shared by all sources
  since: 6h              # RFC 3339, "2006-01-02 15:04", "2006-01-02" or a duration meaning "ago"
  until: ""              # default: now / latest version of each source

sources: [...]
environment: {...}
checks: [...]
bisect: {...}
```

## How states work

Each source produces a **baseline** (its known-good version) and an ordered list of **changes** after it. InfraBisect merges all sources into one timeline ordered by time, keeping each source's own order. **State _k_** is every source at its baseline, with changes 1..k applied. State 0 is the known-good past and state N is the broken present. The bisect looks for the first state that fails, and the change that leads into that state is the culprit.

A source's baseline is chosen in this order:

1. `good` when it is set.
2. The newest version at or before `window.since`.
3. Otherwise, an error.

The bad end is `bad` when it is set. Otherwise it is the newest version at or before `window.until`, or simply the newest version.

## Sources

Every source has a unique `name` matching `[A-Za-z0-9][A-Za-z0-9_.-]*`. Each source is rendered into `<state-dir>/<name>/`.

### `git`: commits in a repository

```yaml
- name: platform
  type: git
  repo: ../platform-config     # local path to the repository
  path: clusters/prod          # optional: only commits touching this path count
  good: v1.8.0                 # any ref; or rely on window.since
  bad: main                    # default HEAD
  firstParent: true            # default true: merges count as one change
  render:
    type: raw                  # raw | kustomize | helm | command
```

| `render.type` | Output | Notes |
|---|---|---|
| `raw` | Files under `path`, copied as-is | Default |
| `kustomize` | `kubectl kustomize <path>` → `manifests.yaml` | Requires `kubectl` |
| `helm` | `helm template <release> <chart> -f values...` → `manifests.yaml` | Settings: `render.chart`, `render.release`, `render.namespace`, `render.values` (paths relative to the repo root) |
| `command` | Whatever your command writes to `$INFRABISECT_OUT_DIR` | Runs in the extracted tree. Variables: `INFRABISECT_SRC_DIR`, `INFRABISECT_VERSION`, `INFRABISECT_SOURCE` |

Each commit is extracted once with `git archive` and cached in `workDir/cache`. Your working copy is never checked out or modified.

**Remedy:** `git revert <sha>`. With `infrabisect run --revert`, InfraBisect also creates a branch `infrabisect/revert-<sha>` in a separate worktree, based on the bad commit, with the culprit already reverted. It never pushes.

### `helm`: revisions of a release

```yaml
- name: payments
  type: helm
  release: payments
  namespace: prod
  kubeContext: prod-cluster    # read access is enough
  good: "41"                   # revision number; or rely on window.since
  bad: "45"                    # default: latest revision
```

The changes come from `helm history`. Each revision is rendered with `helm get manifest --revision N`, which is the exact manifest Helm stored for that revision, so no chart repository access is needed. **Remedy:** `helm rollback <release> <previous revision>`.

### `exec`: anything else

Feature flags, Terraform Cloud state versions, image tags in a registry, DNS records, a SaaS config API. If you can list a system's history and render one version of it, InfraBisect can bisect it.

```yaml
- name: flags
  type: exec
  list: ./scripts/flag-history.sh      # prints JSON, oldest first
  good: v118                           # optional
  render:
    command: ./scripts/render-flags.sh "$INFRABISECT_VERSION" > "$INFRABISECT_OUT_DIR/flags.yaml"
  remedy: flagctl restore {{prev}}     # {{version}}, {{prev}}, {{source}}
```

`list` must print either an array or an object:

```json
[{"version": "v118", "time": "2026-10-27T09:20:00Z", "author": "eli", "summary": "enable new_search"}]
```

```json
{"baseline": "v117", "changes": [ ... ]}
```

`list` receives `INFRABISECT_SINCE` and `INFRABISECT_UNTIL` (RFC 3339) when a window is set, so it can query efficiently.

## Environment

Each state is reproduced in the environment, then checked, then reset.

### `dry`

Nothing is deployed. Checks inspect the rendered files under `$INFRABISECT_STATE_DIR` with policy engines (conftest, kyverno CLI), schema validators (kubeconform) or diff tools. This is the fastest mode and runs anywhere.

### `kind`

```yaml
environment:
  type: kind
  cluster: infrabisect     # default
  nodeImage: kindest/node:v1.31.0
  reuse: false             # true: use an existing cluster with this name instead of creating one
  fresh: false             # true: recreate the cluster for every state (slow, fully isolated)
  keep: false              # true: do not delete the cluster at the end
  loadImages: [registry.example.com/web:1.4]   # side-load images already pulled on the host
  applySources: [app]      # default: every source that rendered .yaml/.yml/.json files
  timeout: 5m              # per-state apply timeout
```

For each state, InfraBisect runs `kubectl apply` on the selected source directories, runs the checks with `KUBECONFIG` pointing at the cluster, and then runs `kubectl delete` on the same objects before the next state. The cluster is created once and deleted at the end, unless `reuse` or `keep` is set.

### `exec`

Hooks for any other sandbox, such as docker compose, a Terraform workspace against LocalStack, or a Nomad dev agent:

```yaml
environment:
  type: exec
  setup: docker compose up -d db          # once
  apply: ./deploy.sh "$INFRABISECT_STATE_DIR"
  reset: docker compose down -v
  teardown: docker compose down -v        # once
```

## Checks

Checks run in order, and the first failing one decides the verdict. Every check receives these variables:

| Variable | Value |
|---|---|
| `INFRABISECT_STATE` | State index _k_ |
| `INFRABISECT_TOTAL` | N |
| `INFRABISECT_STATE_DIR` | Root of the rendered sources |
| `INFRABISECT_SNAPSHOT` | JSON `{source: version}` |
| `INFRABISECT_VERSION_<SOURCE>` | Version of each source (name upper-cased, non-alphanumerics → `_`) |
| `KUBECONFIG` | Set for `kind` environments |

| `type` | Good when | Settings |
|---|---|---|
| `command` | `run` exits 0. Exit **125** marks the state untestable (skipped), exactly like `git bisect run`. | `run`, `timeout` (default 5m) |
| `http` | GET `url` returns `expectStatus` (default 200) and the body contains `bodyContains` | `url`, `expectStatus`, `bodyContains`, `timeout` |
| `rollout` | All Deployments are Available and all StatefulSets/DaemonSets finish rolling out | `namespaces` (default all), `timeout` (default 3m) |

All checks accept `retries` (default 0) and `interval` (default 5s). A check passes as soon as one attempt passes.

## Bisect options

```yaml
bisect:
  verifyBad: true          # test state N first and stop if it is not actually broken
  verifyGood: false        # also test state 0 first
  onRenderError: bad       # bad | skip: a state that fails to render
  onApplyError: bad        # bad | skip: a state the environment rejects
  keepStates: false        # keep rendered states in workDir/states
```

`bad` is the right default for render and apply errors, because a manifest that no longer renders or applies is usually the regression itself. Use `skip` when such failures come from the environment rather than the change.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Culprit found |
| 1 | Error (configuration, environment, interrupted) |
| 2 | Inconclusive, not reproducible, or the baseline is already bad |
| 3 | No changes in the window |
