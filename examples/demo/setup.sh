#!/bin/sh
# Builds the demo incident in .demo/:
#   .demo/repo   a git repo of Kubernetes manifests with 8 commits after the
#                known-good tag. One of them quietly breaks the readiness probe.
#   .demo/flags  a feature-flag history with 4 changes interleaved in time.
# Re-running the script recreates everything from scratch.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
demo="$here/.demo"
rm -rf "$demo"
mkdir -p "$demo/repo/k8s" "$demo/flags"
cd "$demo/repo"

git init -q -b main
git config user.name "demo"
git config user.email "demo@example.com"
git config commit.gpgsign false
git config core.autocrlf false

# Manifest knobs, changed one commit at a time.
REPLICAS=2
IMAGE=nginx:1.27-alpine
RESOURCES=""
PROBE=""
LABELS=""
MEMORY=128Mi
PDB=no

write_manifests() {
  cat > k8s/00-namespace.yaml <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: demo
EOF
  cat > k8s/deployment.yaml <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo
  labels:
    app: web$LABELS
spec:
  replicas: $REPLICAS
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: $IMAGE
          ports:
            - containerPort: 80
          resources:
            limits:
              memory: $MEMORY$RESOURCES$PROBE
EOF
  cat > k8s/service.yaml <<EOF
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: demo
spec:
  selector:
    app: web
  ports:
    - name: http
      port: 80
      targetPort: 80
EOF
  if [ "$PDB" = yes ]; then
    cat > k8s/pdb.yaml <<EOF
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: web
  namespace: demo
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: web
EOF
  fi
}

# commit "<author>" "<UTC time>" "<message>"
commit() {
  write_manifests
  git add -A
  GIT_AUTHOR_NAME="$1" GIT_AUTHOR_DATE="$2" GIT_COMMITTER_DATE="$2" \
    git commit -q -m "$3"
}

commit "Asha"   "2026-10-27T09:00:00Z" "web: initial deployment and service"
git tag known-good

REPLICAS=3
commit "Ben"    "2026-10-27T09:12:00Z" "web: scale to 3 replicas for launch traffic"

RESOURCES='
            requests:
              cpu: 50m
              memory: 64Mi'
commit "Chen"   "2026-10-27T09:31:00Z" "web: add resource requests"

PROBE='
          readinessProbe:
            httpGet:
              path: /
              port: 80
            periodSeconds: 5'
commit "Asha"   "2026-10-27T09:47:00Z" "web: add readiness probe"

IMAGE=nginx:1.27.3-alpine
commit "Dana"   "2026-10-27T10:05:00Z" "web: pin nginx to 1.27.3"

# The culprit: a tidy-looking refactor that points the probe at a port
# nginx does not listen on. Pods never become Ready.
PROBE='
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
            periodSeconds: 5'
commit "Ben"    "2026-10-27T10:22:00Z" "web: standardize health endpoint on /healthz"

PDB=yes
commit "Chen"   "2026-10-27T10:40:00Z" "web: add PodDisruptionBudget"

LABELS='
    app.kubernetes.io/part-of: storefront'
commit "Dana"   "2026-10-27T10:58:00Z" "web: add app.kubernetes.io labels"

MEMORY=256Mi
commit "Asha"   "2026-10-27T11:15:00Z" "web: raise memory limit to 256Mi"

# Feature flags, rendered as a ConfigMap per version.
flag() { # flag <version> <search> <checkout_v2_percent> <banner>
  cat > "$demo/flags/$1.yaml" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: feature-flags
  namespace: demo
data:
  new_search: "$2"
  checkout_v2_percent: "$3"
  legacy_banner: "$4"
EOF
}
flag v0 false 0 true
flag v1 true 0 true
flag v2 true 10 true
flag v3 true 50 true
flag v4 true 50 false
cat > "$demo/flags/history.json" <<EOF
[
  {"version": "v0", "time": "2026-10-27T08:30:00Z", "author": "flags-bot", "summary": "baseline"},
  {"version": "v1", "time": "2026-10-27T09:20:00Z", "author": "Eli", "summary": "enable new_search"},
  {"version": "v2", "time": "2026-10-27T09:55:00Z", "author": "Eli", "summary": "checkout_v2 rollout to 10%"},
  {"version": "v3", "time": "2026-10-27T10:30:00Z", "author": "Farah", "summary": "checkout_v2 rollout to 50%"},
  {"version": "v4", "time": "2026-10-27T11:05:00Z", "author": "Farah", "summary": "disable legacy_banner"}
]
EOF

echo "Demo incident ready in $demo"
echo "  git: $(git rev-list --count known-good..HEAD) commits after known-good"
echo "  flags: 4 changes after v0"
