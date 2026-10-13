#!/bin/sh
# Look-only update check (macOS and Linux counterpart of hermes-update-check.cmd).
# Updates and closes nothing; may fetch commits like git fetch.
exec sh "$(dirname -- "$0")/hermes-safe-update.sh" --check "$@"
