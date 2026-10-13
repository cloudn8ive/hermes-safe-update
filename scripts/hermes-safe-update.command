#!/bin/sh
# macOS: double-click in Finder to run a safe Hermes update in Terminal.
# Keep it in the same folder as hermes-safe-update.sh.
exec sh "$(dirname -- "$0")/hermes-safe-update.sh" "$@"
