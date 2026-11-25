#!/usr/bin/env bash
# Sign a req Baseline for mill. A HUMAN runs this; agents never do (AGENTS.md: ratification is the
# human's act). Prompts for your minisign passphrase once to derive the public key for
# baselines/trust.yml (first run only), and once to sign the manifest.
#
#   scripts/sign-baseline.sh [name]      # name defaults to v0.1.0 -> baselines/v0.1.0.json
set -euo pipefail
cd "$(dirname "$0")/.."

name=${1:-v0.1.0}
manifest="baselines/$name.json"
key_id="req-baseline-1"
secret="${MINISIGN_SECRET_KEY:-$HOME/.minisign/minisign.key}"

req --version | grep -q 'claim_schema *req.claim/4' && req --version | grep -q 'rules_v *9' \
  || { echo "req must report claim_schema req.claim/4 and rules_v 9; stop" >&2; exit 2; }
command -v minisign >/dev/null || { echo "minisign not found" >&2; exit 2; }
[ -f "$secret" ] || { echo "no minisign secret key at $secret (set MINISIGN_SECRET_KEY)" >&2; exit 2; }
[ -e "$manifest" ] && { echo "$manifest already exists; pick another name" >&2; exit 2; }

echo "== 1. trust anchor (baselines/trust.yml)"
mkdir -p baselines
if [ ! -f baselines/trust.yml ]; then
  pub=$(mktemp)
  minisign -R -s "$secret" -p "$pub" -f >/dev/null
  {
    echo "# The trust anchor for Baselines (req ADR 0010). Public keys only; reviewed like code."
    echo "keys:"
    echo "  - id: \"$key_id\""
    echo "    scheme: \"minisign\""
    echo "    public_key: |"
    sed 's/^/      /' "$pub"
    echo "    valid_from: \"$(date -u +%Y-%m-%d)\""
  } > baselines/trust.yml
  rm -f "$pub"
  echo "   wrote baselines/trust.yml"
else
  echo "   exists, kept"
fi

if [ -d decisions ] && ls decisions/DEC-*.yml >/dev/null 2>&1; then
  echo "== 2. decision log: seal the hash chain"
  req decision --fill
  req decision --verify
fi

echo "== 3. manifest"
req baseline --at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --out "$manifest"

echo "== 4. sign (prompts for your passphrase)"
minisign -Sm "$manifest" -s "$secret"

echo "== 5. verify"
req baseline --verify "$manifest" --key "$key_id"

echo
echo "Signed. Review and commit yourself:"
echo "  git add baselines/ decisions/ && git commit -m 'chore(baseline): sign $name' && git push origin main"
