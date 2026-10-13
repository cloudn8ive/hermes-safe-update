#!/bin/sh
# hermes-safe-update hook wrapper (macOS/Linux) for bundled_skill_merge.py.
# Runs the script with the Python that hermes-safe-update found for Hermes: the variable
# HERMES_SAFE_UPDATE_PYTHON, looked up when the hook starts (never a fixed path: the folder name of
# Hermes' Python changes on updates). HERMES_SAFE_UPDATE_HOME and _CHECKOUT are read by the script.
#
#   no argument      = merge (what the hook runs)
#   list | keep NAME | unkeep NAME | merge --dry-run   = manual use, see README.md
#
# Outside a hook set HERMES_SAFE_UPDATE_PYTHON and HERMES_SAFE_UPDATE_HOME first.
# The script's LAST output line becomes the summary-card row; exit code 1 = a warning on the card.
# In safe-update.yaml name the shell: run: ["/bin/sh", "/absolute/path/bundled-skill-merge.sh"]
if [ -z "${HERMES_SAFE_UPDATE_PYTHON:-}" ] || [ ! -x "$HERMES_SAFE_UPDATE_PYTHON" ]; then
  echo "bundled skills: skipped - HERMES_SAFE_UPDATE_PYTHON is not set to an existing Python (run this as a hermes-safe-update hook, or set it yourself)"
  exit 1
fi
here=$(cd -- "$(dirname -- "$0")" && pwd) || exit 1
PYTHONUTF8=1
export PYTHONUTF8
if [ "$#" -eq 0 ]; then
  set -- merge
fi
exec "$HERMES_SAFE_UPDATE_PYTHON" -W ignore "$here/bundled_skill_merge.py" "$@"
