#!/bin/sh
# Create .env from .env.example with freshly generated random secrets. Never overwrites.
set -eu
cd "$(dirname "$0")/.."
if [ -f .env ]; then
  echo ".env already exists; leaving it untouched."
  exit 0
fi
rand() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
pg="$(rand)"
rd="$(rand)"
jwt="$(rand)$(rand)"
sed -e "s/CHANGE_ME_POSTGRES/${pg}/g" -e "s/CHANGE_ME_REDIS/${rd}/g" -e "s/CHANGE_ME_JWT/${jwt}/g" .env.example > .env
chmod 600 .env
echo "Created .env with generated secrets."
