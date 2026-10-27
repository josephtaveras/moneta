#!/usr/bin/env bash
# Local onboarding only. Tokens are never command arguments or persisted.
set -euo pipefail
set +x
umask 077
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
mkdir -p .local
chmod 700 .local
if [[ ! -f .local/simplefin.env ]]; then
  if [[ -e .local/simplefin.db ]]; then
    printf '%s\n' 'Existing SimpleFIN database found without its key file. Restore .local/simplefin.env with the original encryption key before continuing.' >&2
    exit 1
  fi
  encryption_key="$(openssl rand -base64 32)"
  (
    set -o noclobber
    printf 'export MONETA_ENCRYPTION_KEY=%q\nexport MONETA_DB_PATH=%q\n' \
      "$encryption_key" "$repo_dir/.local/simplefin.db" > .local/simplefin.env
  )
  unset encryption_key
fi
chmod 600 .local/simplefin.env
# shellcheck source=/dev/null
source .local/simplefin.env
go build -o .local/moneta ./cmd/moneta
if .local/moneta simplefin-accounts --cached >/dev/null 2>&1; then
  printf '%s\n' 'Existing SimpleFIN connection found; keeping its credential.'
else
  printf '%s' 'Paste the SimpleFIN setup token (input hidden), then press Enter: ' >&2
  IFS= read -r -s SIMPLEFIN_SETUP_TOKEN
  printf '\n' >&2
  export SIMPLEFIN_SETUP_TOKEN
  trap 'unset SIMPLEFIN_SETUP_TOKEN' EXIT
  .local/moneta link
  unset SIMPLEFIN_SETUP_TOKEN
fi
printf '%s\n' 'Linked. Discovering accounts for type/sign confirmation; no transactions will be imported yet.'
.local/moneta simplefin-accounts
printf '%s\n' 'Next: tell your agent "SimpleFIN linked". Do not paste the token, encryption key, or account output into chat.'
