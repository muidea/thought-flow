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
  awk '
    function emit(line) {
      print line
      printed = 1
    }
    function indent(line, prefix) {
      prefix = line
      sub(/[^ ].*$/, "", prefix)
      return length(prefix)
    }
    /^[[:space:]]*not ok[[:space:]][0-9]+/ {
      in_failure = 1
      failure_indent = indent($0)
      emit($0)
      next
    }
    in_failure {
      current_indent = indent($0)
      if ($0 ~ /^[[:space:]]*(ok|not ok)[[:space:]][0-9]+/ && current_indent <= failure_indent) {
        if ($0 ~ /^[[:space:]]*not ok[[:space:]][0-9]+/) {
          failure_indent = current_indent
          emit($0)
          next
        }
        in_failure = 0
      } else if ($0 ~ /^[^[:space:]#]/ && $0 !~ /^(Error:|make(\[[0-9]+\])?:)/) {
        in_failure = 0
      } else {
        emit($0)
        next
      }
    }
    /^(node|go|CGO_LDFLAGS=)/ { emit($0); next }
    /^# (tests|suites|pass|fail|cancelled|skipped|todo|duration_ms)[[:space:]]/ { emit($0); next }
    /^(make(\[[0-9]+\])?: .*Error|Error:)/ { emit($0); next }
    END { exit(printed ? 0 : 1) }
  ' "$log_file" >&2 || cat "$log_file" >&2
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
