#!/usr/bin/env bash
# Sign a CI draft locally. The private key never leaves this machine.
set -euo pipefail
if [[ $# -ne 1 || ! "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: $0 vMAJOR.MINOR.PATCH" >&2
  exit 2
fi
release_tag="$1"
release_repo="${KILNBRIDGE_RELEASE_REPO:-Jourloy/kilnbridge}"
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
git diff --quiet
git diff --cached --quiet
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse "$release_tag^{commit}")" ]]; then
  echo 'Check out the release tag before signing its draft.' >&2
  exit 1
fi
if [[ "$(gh release view "$release_tag" --repo "$release_repo" --json isDraft --jq .isDraft)" != true ]]; then
  echo 'Only unpublished draft releases can be signed by this helper.' >&2
  exit 1
fi
release_dir="$(mktemp -d)"
trap 'rm -rf "$release_dir"' EXIT
gh release download "$release_tag" --repo "$release_repo" --dir "$release_dir" \
  --pattern kilnbridge-windows-amd64.exe --pattern SHA256SUMS.txt
go run ./cmd/kilnbridge sign-release --version "${release_tag#v}" \
  --binary "$release_dir/kilnbridge-windows-amd64.exe" --out "$release_dir"
# Retain the CI checksums for other platforms and add the signed update metadata.
(cd "$release_dir" && shasum -a 256 update.json update.json.sig >> SHA256SUMS.txt)
gh release upload "$release_tag" --repo "$release_repo" --clobber \
  "$release_dir/update.json" "$release_dir/update.json.sig" "$release_dir/SHA256SUMS.txt"
gh release edit "$release_tag" --repo "$release_repo" --draft=false
