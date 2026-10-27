package cli

import "sort"

var templates = map[string]string{
	"kind": `# InfraBisect configuration - replays every candidate state in a kind cluster.
# Reference: https://github.com/pughal-prog/infrabisect/blob/main/docs/configuration.md
version: 1

# The incident window. Every source's baseline is its last version before
# "since", unless the source sets good explicitly. Durations mean "ago".
window:
  since: 24h

sources:
  - name: manifests
    type: git
    repo: .                # path to the repository holding your manifests
    path: deploy/k8s       # only commits touching this path count as changes
    render:
      type: raw            # raw | kustomize | helm | command

environment:
  type: kind
  cluster: infrabisect
  timeout: 5m

checks:
  - name: rollout
    type: rollout          # every Deployment/StatefulSet/DaemonSet becomes ready
    timeout: 2m
`,
	"dry": `# InfraBisect configuration - static checks on rendered manifests, no cluster.
version: 1

sources:
  - name: manifests
    type: git
    repo: .
    path: deploy/k8s
    good: v1.0.0           # known-good tag, branch or commit
    bad: HEAD
    render:
      type: kustomize

environment:
  type: dry

checks:
  # Any command works: conftest, kubeconform, polaris, a grep. Exit 0 = good,
  # 125 = cannot test this state (skipped), anything else = bad.
  - name: policy
    type: command
    run: conftest test "$INFRABISECT_STATE_DIR/manifests" --policy policy/
`,
	"helm": `# InfraBisect configuration - bisect Helm release history plus a manifest repo.
version: 1

window:
  since: "2026-01-01 09:00"

sources:
  - name: payments
    type: helm
    release: payments
    namespace: prod
    kubeContext: prod-cluster   # read-only access is enough: history + manifests

  - name: platform
    type: git
    repo: ../platform-config
    path: clusters/prod

  # Anything else that changes: feature flags, Terraform state versions,
  # image tags. list prints JSON [{version, time, author, summary}], oldest first.
  - name: flags
    type: exec
    list: ./scripts/flag-history.sh
    render:
      command: ./scripts/render-flags.sh "$INFRABISECT_VERSION" > "$INFRABISECT_OUT_DIR/flags.yaml"
    remedy: ./scripts/set-flags.sh {{prev}}

environment:
  type: kind
  cluster: infrabisect-payments
  loadImages: []

checks:
  - type: rollout
    timeout: 3m
  - name: smoke
    type: command
    run: ./scripts/smoke-test.sh
    retries: 3
    interval: 10s
`,
}

func templateNames() []string {
	names := make([]string, 0, len(templates))
	for n := range templates {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
