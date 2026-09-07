#!/usr/bin/env bash
# Run the existing Android release and a Kilnbridge Windows job together.
set -euo pipefail
if [[ $# -ne 4 ]]; then
  echo "Usage: $0 FLUTTER_SOURCE WINDOWS_RECIPE ENV_FILE ANDROID_RELEASE_SCRIPT" >&2
  exit 2
fi
source_dir="$(cd "$1" && pwd)"
recipe="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
env_file="$(cd "$(dirname "$3")" && pwd)/$(basename "$3")"
android_script="$(cd "$(dirname "$4")" && pwd)/$(basename "$4")"
client="${KILNBRIDGE_BIN:-kilnbridge}"
# Finish the source snapshot before Android preparation changes generated files.
submission="$("$client" submit --detach --source "$source_dir" --recipe "$recipe" --env "$env_file")"
printf '%s\n' "$submission"
if [[ ! "$submission" =~ Job:\ ([a-f0-9-]+) ]]; then
  echo "Kilnbridge did not return a job ID." >&2
  exit 1
fi
windows_job="${BASH_REMATCH[1]}"
"$client" wait --job "$windows_job" --env "$env_file" &
windows_sender_pid=$!
# The existing Android script owns signing and its upload. It reads its own .env.
android_result=0
(cd "$source_dir" && "$android_script") || android_result=$?
windows_result=0
wait "$windows_sender_pid" || windows_result=$?
printf 'Android exit: %s; Windows exit: %s\n' "$android_result" "$windows_result"
if [[ $android_result -ne 0 || $windows_result -ne 0 ]]; then
  exit 1
fi
