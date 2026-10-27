#!/bin/sh
# A stand-in for a feature-flag service (LaunchDarkly, Unleash, Flagd...).
#   list    print the change history as JSON, oldest first
#   render  write the flags at $INFRABISECT_VERSION into $INFRABISECT_OUT_DIR
#   set     what an on-call engineer would run to restore a version
set -eu
here=$(cd "$(dirname "$0")" && pwd)
flags="$here/.demo/flags"

case "${1:-}" in
  list)   cat "$flags/history.json" ;;
  render) cp "$flags/$INFRABISECT_VERSION.yaml" "$INFRABISECT_OUT_DIR/flags.yaml" ;;
  set)    echo "would restore feature flags to ${2:?version}" ;;
  *)      echo "usage: flags.sh list|render|set <version>" >&2; exit 2 ;;
esac
