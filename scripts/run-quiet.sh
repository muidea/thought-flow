#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <label> <command> [args...]" >&2
  exit 2
fi

label="$1"
shift

log_file="$(mktemp)"
trap 'rm -f "$log_file"' EXIT

print_failure_log() {
  # A failed check is the exceptional path: preserve its complete output.
  # Go emits failures as "--- FAIL:" / panic / package-level "FAIL", which
  # is not TAP and was previously discarded by the selective formatter.
  cat "$log_file" >&2
}

printf 'ci: running %s\n' "$label"
if "$@" >"$log_file" 2>&1; then
  printf 'ci: ok %s\n' "$label"
  exit 0
else
  status="$?"
fi

printf 'ci: failed %s\n' "$label" >&2
print_failure_log
exit "$status"
