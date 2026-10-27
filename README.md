<div align="center">

# InfraBisect

**`git bisect` for production infrastructure.**

Something broke after a day of 40 changes: Terraform applies, Helm upgrades, manifest commits, feature flags.<br>
InfraBisect replays them in a throwaway environment and tells you **which one** did it, in about log₂(N) steps.

[![ci](https://github.com/pughal-prog/infrabisect/actions/workflows/ci.yml/badge.svg)](https://github.com/pughal-prog/infrabisect/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pughal-prog/infrabisect.svg)](https://pkg.go.dev/github.com/pughal-prog/infrabisect)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

</div>

```console
$ infrabisect run --report incident.md
Bisecting 12 changes across 2 sources in dry (about 4 steps, +1 to confirm the failure)
  step 1  state 12/12  after app@345e4da774da   bad    402ms  probe-ports: readinessProbe targets port 8080 but the container listens on 80
  step 2  state  6/12  after app@7b45ccd6fc9c   good   398ms  all checks passed
  step 3  state  9/12  after app@2bfbd4db0f41   bad    413ms  probe-ports: readinessProbe targets port 8080 ...
  step 4  state  7/12  after app@34bbcc57f990   bad    414ms  probe-ports: readinessProbe targets port 8080 ...

✖ Culprit found after 4 steps in 1.9s

  change   7 of 12
  source   app (git)
  version  34bbcc57f990718b78b59ba78105edfb6e506e57  (was 7b45ccd6fc9c)
  when     2026-10-27 10:22:00 UTC
  author   Ben
  summary  web: standardize health endpoint on /healthz
  failure  probe-ports: exit 1: readinessProbe targets port 8080 but the container listens on 80

Fix: Revert commit 34bbcc57f990 ("web: standardize health endpoint on /healthz") in .demo/repo
  $ git -C .demo/repo revert --no-edit 34bbcc57f990718b78b59ba78105edfb6e506e57

Timeline  ✔ good  ✖ bad  ~ skipped  ▶ culprit
  ·   1  10-27 09:12  app    be939745fafd  web: scale to 3 replicas for launch traffic
  ·   2  10-27 09:20  flags  v1            enable new_search
  ·   3  10-27 09:31  app    229fdca3b5a7  web: add resource requests
  ·   4  10-27 09:47  app    a95a7d5acc2c  web: add readiness probe
  ·   5  10-27 09:55  flags  v2            checkout_v2 rollout to 10%
  ✔   6  10-27 10:05  app    7b45ccd6fc9c  web: pin nginx to 1.27.3
  ▶   7  10-27 10:22  app    34bbcc57f990  web: standardize health endpoint on /healthz
  ·   8  10-27 10:30  flags  v3            checkout_v2 rollout to 50%
  ✖   9  10-27 10:40  app    2bfbd4db0f41  web: add PodDisruptionBudget
  ·  10  10-27 10:58  app    ea132a282fcd  web: add app.kubernetes.io labels
  ·  11  10-27 11:05  flags  v4            disable legacy_banner
  ✖  12  10-27 11:15  app    345e4da774da  web: raise memory limit to 256Mi
```

<sub>Output of the bundled demo (`make demo`): 8 manifest commits and 4 feature-flag changes, merged into one timeline. Commit hashes differ on each run.</sub>

---

## Why

Every incident review asks the same question: *what changed?* The answer is usually scattered across several places:

- commits in a GitOps repo
- `helm history` of a dozen releases
- a feature-flag dashboard
- a Terraform state history
- whatever someone ran by hand

Teams line these up by hand in a Slack thread and guess.

`git bisect` solved this for code twenty years ago, but it only understands one repository. Production is many systems changing at once.

**InfraBisect merges every change source into one timeline and bisects all of them together.** Each candidate state is a consistent snapshot ("repo at `7b45ccd`, release `payments` at revision 14, flags at `v2`"). It is rendered, deployed to a disposable environment, and checked. A timeline of 40 changes needs about 6 probes. A timeline of 1,000 needs 10.

## Features

- **Multi-source timelines.** Git commits, Helm release revisions, and an `exec` source for anything with a history (feature flags, Terraform Cloud state versions, image tags, DNS, SaaS configs).
- **Real reproduction.** Each state can run in a throwaway **kind** cluster, in your own sandbox through **exec** hooks (docker compose, LocalStack, a cloud test account), or **dry**, where static checks run on the rendered manifests.
- **Checks you already have.** Any command (exit `0` good, `125` untestable, anything else bad), HTTP probes, and Kubernetes rollout health, with retries.
- **Skips handled like `git bisect run`.** Untestable states are routed around. If they hide the boundary, InfraBisect reports the candidate changes instead of guessing.
- **Remedies, not just blame.** It prints the exact `git revert` / `helm rollback` / custom undo command. `--revert` also prepares a revert branch in a separate worktree. It never pushes.
- **Incident-ready reports.** Colored terminal output, plus Markdown for the postmortem or ticket and JSON for automation.
- **Safe by design.** Your checkout is never modified (versions come from `git archive`). Helm states come from stored manifests with read-only access. Nothing touches production.
- **Small footprint.** A single static binary with one dependency (`yaml.v3`). It shells out to the `git`/`kubectl`/`helm`/`kind` you already use, so authentication just works.

## Install

```bash
go install github.com/pughal-prog/infrabisect/cmd/infrabisect@latest
```

Or download a binary from [Releases](https://github.com/pughal-prog/infrabisect/releases), or use the container image:

```bash
docker run --rm -v "$PWD:/work" ghcr.io/pughal-prog/infrabisect run
```

## Try it in 30 seconds

The repo ships a realistic incident. It has 8 manifest commits and 4 feature-flag changes, and one of them quietly points a readiness probe at the wrong port:

```bash
git clone https://github.com/pughal-prog/infrabisect && cd infrabisect
make demo                      # static checks, no cluster needed
make demo-kind                 # the same incident in a real kind cluster
```

## Quick start

```bash
infrabisect init --template kind    # or: dry, helm
infrabisect validate
infrabisect timeline --since 6h     # see what changed, test nothing
infrabisect run --since 6h --report incident.md
```

A config that bisects a GitOps repo, a Helm release and feature flags together:

```yaml
version: 1

window:
  since: "2026-10-27 09:00"        # baseline = each source's last version before this

sources:
  - name: platform
    type: git
    repo: ../platform-config
    path: clusters/prod            # only commits touching this path

  - name: payments
    type: helm
    release: payments
    namespace: prod
    kubeContext: prod              # read-only: history + stored manifests

  - name: flags
    type: exec
    list: ./scripts/flag-history.sh          # JSON [{version, time, author, summary}]
    render:
      command: ./scripts/render-flags.sh "$INFRABISECT_VERSION" > "$INFRABISECT_OUT_DIR/flags.yaml"
    remedy: flagctl restore {{prev}}

environment:
  type: kind                       # dry | kind | exec

checks:
  - type: rollout                  # every workload becomes ready
    timeout: 2m
  - name: smoke
    type: command
    run: ./scripts/smoke.sh        # exit 0 good, 125 skip, else bad
    retries: 2
```

Full reference: [docs/configuration.md](docs/configuration.md).

## How it works

```
 git ──┐                                     state k = every source at its version after change k
 helm ─┼─► per-source streams ─► merged timeline ─► binary search ─► render ─► apply ─► check ─► reset
 exec ─┘    (baseline + changes)  c1 … cN          (with skips)      (dry | kind | exec)
```

1. **Collect.** Each source reports its known-good baseline and the ordered changes after it.
2. **Merge.** A k-way merge by time builds one timeline and never reorders a source's own history. State *k* is the baseline with changes 1..k applied.
3. **Bisect.** The search confirms that state N fails, then probes the middle state, narrowing until one change separates a passing state from a failing one.
4. **Reproduce.** For every probe, every source is rendered at its version, applied to the environment, checked, then cleaned up.
5. **Report.** You get the culprit, its author and time, the failing check output, and the exact undo command.

Details: [docs/architecture.md](docs/architecture.md).

## Commands

| Command | What it does |
|---|---|
| `infrabisect run` | Bisect and report. Flags: `--since`, `--until`, `--report FILE.md`, `--json FILE\|-`, `--revert`, `--verify-good`, `--no-verify`, `--keep-states`, `--verbose` |
| `infrabisect timeline` | Print the merged timeline (`--json` for machines) |
| `infrabisect render --state K` | Materialize one state to a directory to inspect or debug it by hand |
| `infrabisect validate` | Check the config and report every error at once |
| `infrabisect init --template kind\|dry\|helm` | Write a starter config |

Exit codes: `0` culprit found, `2` inconclusive / not reproducible, `3` no changes, `1` error.

## In CI: GitHub Action

Bisect automatically when a nightly environment check fails, and put the report in the job summary:

```yaml
- uses: pughal-prog/infrabisect@v0
  id: bisect
  with:
    config: infrabisect.yaml
    since: 24h
- run: echo "Culprit: ${{ steps.bisect.outputs.culprit }}"
  if: always()
```

## Use cases

- **Incident response.** "It broke between 9 and 11." Get the culprit before the war room fills up.
- **Postmortems.** Attach the Markdown report as evidence instead of a reconstructed timeline.
- **Upgrade regressions.** Bisect a Kubernetes add-on bump that touched 30 manifests.
- **Policy drift.** Use `dry` mode with conftest or kubeconform to find when a manifest started violating policy.
- **Flaky environments.** Exit `125` from your check when the sandbox itself misbehaves. InfraBisect works around it.

## Roadmap

- [ ] Sources: Argo CD app history, Flux, Terraform Cloud/HCP state versions, container registry tags, AWS CloudTrail
- [ ] Environments: vcluster, LocalStack + Terraform, docker compose presets
- [ ] Parallel probing across multiple clusters (k-ary search)
- [ ] Render cache by (source, version), and incremental apply between adjacent states
- [ ] Optional LLM explanation of *why* the culprit broke the checks
- [ ] Slack/Teams notification with the report

## Contributing

Issues and PRs are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). New sources are the most valuable contributions: if your system has a history, it can be bisected.

## License

[Apache 2.0](LICENSE)
