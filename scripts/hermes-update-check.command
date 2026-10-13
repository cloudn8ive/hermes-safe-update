#!/bin/sh
# macOS: double-click in Finder for the look-only update check in Terminal.
# Keep it in the same folder as hermes-update-check.sh and hermes-safe-update.sh.
exec sh "$(dirname -- "$0")/hermes-update-check.sh" "$@"
