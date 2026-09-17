#!/bin/sh
# Prints the CHANGELOG section for a version, ready to use as release notes.
#
#   scripts/release-notes.sh 0.6.0
#
# The section runs from its own heading to the next one. Reference-style link
# definitions the section uses are appended, because they live at the foot of
# the file and would otherwise render as literal brackets in the release.
#
# Exits 1 when the version has no section, which lets a caller fall back to
# generated notes instead of publishing an empty release.
set -eu

VERSION="${1:-}"
[ -n "$VERSION" ] || { printf 'usage: %s <version>\n' "$0" >&2; exit 2; }
VERSION="${VERSION#v}"

CHANGELOG="${CHANGELOG_FILE:-CHANGELOG.md}"
[ -f "$CHANGELOG" ] || { printf '%s not found\n' "$CHANGELOG" >&2; exit 2; }

section=$(awk -v version="$VERSION" '
  # Start at the heading for this version and stop at the next heading, or at
  # the block of link definitions that closes the file after the last section.
  $0 ~ "^## \\[" version "\\]" { inside = 1; next }
  inside && /^## \[/ { exit }
  inside && /^\[[^]]+\]: / { exit }
  inside { print }
' "$CHANGELOG")

# Trim leading and trailing blank lines.
section=$(printf '%s\n' "$section" | sed -e '/./,$!d' | awk '{ lines[NR] = $0 } END { last = NR; while (last > 0 && lines[last] ~ /^[[:space:]]*$/) last--; for (i = 1; i <= last; i++) print lines[i] }')

[ -n "$section" ] || exit 1

printf '%s\n' "$section"

# Append only the definitions this section actually references.
refs=$(printf '%s\n' "$section" | grep -oE '\[[^]]+\]' | sort -u | tr -d '[]' || true)
appended=0
for ref in $refs; do
  definition=$(grep -E "^\[${ref}\]: " "$CHANGELOG" 2>/dev/null || true)
  [ -n "$definition" ] || continue
  if [ "$appended" = 0 ]; then printf '\n'; appended=1; fi
  printf '%s\n' "$definition"
done
