#!/bin/sh
# Safe Hermes update: runs hermes-safe-update outside the desktop app.
# macOS and Linux counterpart of hermes-safe-update.cmd:
#   hermes-safe-update.sh --check   (pre-flight only: no update; may fetch commits like git fetch)
#   hermes-safe-update.sh           (asks, then closes Hermes and updates)
# The binary is looked up next to this file, then in ~/.local/bin, then on
# PATH. Meant as the target of a launcher (hermes-safe-update.desktop on
# Linux, hermes-safe-update.command on macOS); from a terminal you can run
# the binary directly. HERMES_SAFE_UPDATE_NO_PAUSE=1 skips the final
# "Press Enter".

# Like the .cmd shim: always update the default install, whatever
# HERMES_HOME the launching environment carries.
unset HERMES_HOME

dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
self="$dir/$(basename -- "$0")"

pause() {
  if [ "${HERMES_SAFE_UPDATE_NO_PAUSE:-}" != "1" ] && [ -t 0 ] && [ -t 1 ]; then
    printf 'Press Enter to close this window.'
    read -r _ || true
  fi
}

exe=""
for c in "$dir/hermes-safe-update" "$HOME/.local/bin/hermes-safe-update" "$(command -v hermes-safe-update 2>/dev/null)"; do
  # never pick this shim itself (someone may have renamed it without .sh)
  if [ -n "$c" ] && [ "$c" != "$self" ] && [ -f "$c" ] && [ -x "$c" ]; then
    exe=$c
    break
  fi
done
if [ -z "$exe" ]; then
  echo "hermes-safe-update not found next to $(basename -- "$0"), in ~/.local/bin or on PATH (download it and run chmod +x on it)"
  pause
  exit 2
fi

title="Hermes Safe Update"
[ "${1:-}" = "--check" ] && title="Hermes Update Check"
[ -t 1 ] && printf '\033]0;%s\007' "$title"

"$exe" "$@"
rc=$?
echo
echo "Finished with exit code $rc. Log: $HOME/.hermes/logs/safe-update.log"
pause
exit "$rc"
