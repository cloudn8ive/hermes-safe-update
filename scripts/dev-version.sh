#!/usr/bin/env bash
# Print the version a LOCAL build should report (scripts/build-all.sh calls
# this when it is not given a release version):
#
#   v0.1.2                  exactly the latest released version, clean tree
#   v0.1.2+25.3a8a2c7       25 commits after it, at commit 3a8a2c7
#   v0.1.2+25.3a8a2c7.dirty ... with uncommitted changes to tracked files
#   v0.8.0-dev+4.3a8a2c7    the top heading is still marked "Unreleased"
#                           ("## [0.8.0] - Unreleased"): a build of the work
#                           toward v0.8.0, 4 commits after the heading was added
#
# The latest released version is the newest "## [x.y.z]" heading in
# CHANGELOG.md. "Commits after it" counts from the tag v<x.y.z> when the
# repository has one, else from the commit that added that heading (the
# release commit), so it works in a clone without tags. Prints plain "dev"
# when there is no git history to read (e.g. an unpacked source archive).
#
# Release builds do NOT use this: they pass the version explicitly
# (scripts/build-all.sh v0.1.2), which is why release binaries are
# byte-identical between the dev repo and the public clone.
set -euo pipefail

cd "$(dirname "$0")/.."

heading=$(sed -n '/^## \[[0-9]/{s/\r$//;p;q;}' CHANGELOG.md 2>/dev/null || true)
base=$(printf '%s\n' "$heading" | sed -n 's/^## \[\([0-9.]*\)\].*/\1/p')
pre=""
case "$heading" in *[Uu]nreleased*) pre="-dev" ;; esac
if [ -z "$base" ] || ! git rev-parse --verify -q HEAD >/dev/null 2>&1; then
  echo dev
  exit 0
fi

anchor=""
if git rev-parse --verify -q "refs/tags/v${base}^{commit}" >/dev/null 2>&1; then
  anchor=$(git rev-parse "refs/tags/v${base}^{commit}")
else
  # Oldest commit that introduced the heading (git log lists newest first).
  anchor=$(git log -S"$heading" --format=%H -- CHANGELOG.md 2>/dev/null | tail -n 1 || true)
fi

sha=$(git rev-parse --short=7 HEAD)
dirty=""
git diff-index --quiet HEAD -- 2>/dev/null || dirty=".dirty"

if [ -z "$anchor" ]; then
  echo "v${base}${pre}+${sha}${dirty}" # base known, distance unknown
  exit 0
fi
n=$(git rev-list --count "${anchor}..HEAD")
if [ "$n" = 0 ] && [ -z "$dirty" ]; then
  echo "v${base}${pre}"
else
  echo "v${base}${pre}+${n}.${sha}${dirty}"
fi
