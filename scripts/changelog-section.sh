#!/usr/bin/env sh
# Prints the CHANGELOG.md section for one version, without its heading.
#
#   scripts/changelog-section.sh v2.0.0
#
# The leading "v" is optional on both the argument and the heading, and a
# heading may carry a trailing date ("## v2.0.0 - 2026-09-08").
set -eu

version="${1:?usage: changelog-section.sh <version>}"
changelog="${2:-CHANGELOG.md}"

awk -v want="${version#v}" '
  /^## / {
    heading = substr($0, 4)
    sub(/^v/, "", heading)
    split(heading, field, /[ \t]/)
    if (field[1] == want) { inside = 1; next }
    if (inside) { exit }
    next
  }
  inside { print }
' "$changelog" |
  # Trim blank lines from both ends.
  sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
