#!/usr/bin/env bash
# changed-activities.sh <base-ref>
#
# Prints, one per line, the activity selectors (bare activity IDs, e.g.
# `lab.docker.basics`) whose YAML under content/activities/ OR whose
# reference-solution tree under content/activities/solutions/<id>/
# changed relative to <base-ref>.
#
# Used by .github/workflows/content-ci.yml on pull_request to run
# content-ci.ts against only what the PR touched. Empty output means
# "nothing content-relevant changed" and the caller should skip.
#
# Portable to bash 3.2 (macOS) -- no mapfile.
set -euo pipefail

BASE_REF="${1:?usage: changed-activities.sh <base-ref>}"

MERGE_BASE="$(git merge-base HEAD "$BASE_REF")"

git diff --name-only "$MERGE_BASE" HEAD | while IFS= read -r f; do
  case "$f" in
    content/activities/*.yaml)
      base="${f#content/activities/}"
      printf '%s\n' "${base%.yaml}"
      ;;
    content/activities/solutions/*/*)
      rest="${f#content/activities/solutions/}"
      printf '%s\n' "${rest%%/*}"
      ;;
  esac
done | sort -u
