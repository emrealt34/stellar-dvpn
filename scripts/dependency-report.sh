#!/usr/bin/env bash
# Generates docs/DEPENDENCIES.md: direct dependencies, pinned versions and
# available updates for the Go backend, Astro/React web app and Soroban
# contract, plus the current vulnerability scan summaries. Designed to run in
# CI (dependencies.yml) and locally from the repository root.
set -euo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || dirname "$0"/..)"
mkdir -p docs
OUT=docs/DEPENDENCIES.md
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

{
  echo "# Dependency & vulnerability report"
  echo
  echo "_Generated ${NOW} by \`scripts/dependency-report.sh\`. Direct dependencies only;"
  echo "transitive trees are pinned in the respective lock files._"
  echo

  echo "## Go backend (\`backend/\`)"
  echo
  if command -v go >/dev/null 2>&1; then
    echo "| Module | Pinned | Update available |"
    echo "| --- | --- | --- |"
    ( cd backend && go list -m -u -f '{{if and (not .Indirect) .Version}}{{.Path}}|{{.Version}}|{{if .Update}}{{.Update.Version}}{{else}}up to date{{end}}{{end}}' all 2>/dev/null ) \
      | sed 's/^/| /; s/|/ | /g; s/$/ |/'
    echo
    echo "Vulnerability scan:"
    echo '```'
    if command -v govulncheck >/dev/null 2>&1; then
      ( cd backend && govulncheck ./... ) 2>&1 | tail -n 20 || true
    else
      echo "govulncheck not installed; CI runs golang/govulncheck-action."
    fi
    echo '```'
  else
    echo "Go toolchain not available in this environment."
  fi
  echo

  echo "## Web app (\`web/\`, Astro + React)"
  echo
  if command -v npm >/dev/null 2>&1 && [ -f web/package.json ]; then
    ( cd web && python3 ../scripts/webdeps.py )
    echo
    echo "Vulnerability scan (build toolchain advisories are noted separately; the deployed"
    echo "site is a static build that ships only the React runtime, not the dev server):"
    echo '```'
    ( cd web && npm audit --omit=dev 2>&1 | tail -n 15 ) || true
    echo '```'
  else
    echo "npm not available."
  fi
  echo

  echo "## Soroban contract (\`contract/\`)"
  echo
  if [ -f contract/Cargo.toml ]; then
    echo "| Crate | Requirement |"
    echo "| --- | --- |"
    awk '/\[dependencies\]/{f=1;next} /^\[/{f=0} f && /^[a-zA-Z]/{gsub(/[ "]/,"");print "| "$0" |"}' contract/Cargo.toml \
      | sed 's/=/ | /'
    echo
    if command -v cargo-audit >/dev/null 2>&1 || command -v cargo >/dev/null 2>&1; then
      echo '```'
      ( command -v cargo-audit >/dev/null 2>&1 && cd contract && cargo audit 2>&1 | tail -n 15 ) \
        || echo "cargo-audit runs in CI (non-blocking advisory check)."
      echo '```'
    fi
  fi
} > "$OUT"

echo "wrote $OUT"
