# Dependency & vulnerability report

_Generated 2026-10-27T09:39:03Z by `scripts/dependency-report.sh`. Direct dependencies only;
transitive trees are pinned in the respective lock files._

## Go backend (`backend/`)

| Module | Pinned | Update available |
| --- | --- | --- |
 |  github.com/golang-jwt/jwt/v5 | v5.3.1 | up to date |
 |  github.com/oschwald/geoip2-golang | v1.13.0 | up to date |
 |  github.com/stellar/go-stellar-sdk | v0.8.0 | up to date |
 |  modernc.org/sqlite | v1.61.0 | up to date |

Vulnerability scan:
```
govulncheck not installed; CI runs golang/govulncheck-action.
```

## Web app (`web/`, Astro + React)

| Package | Pinned | Latest |
| --- | --- | --- |
| @astrojs/react | 7.1.1 | up to date |
| astro | 7.4.2 | up to date |
| react | 19.3.1 | up to date |
| react-dom | 19.3.1 | up to date |

Vulnerability scan (build toolchain advisories are noted separately; the deployed
site is a static build that ships only the React runtime, not the dev server):
```
found 0 vulnerabilities
```

## Soroban contract (`contract/`)

| Crate | Requirement |
| --- | --- |
| soroban-sdk | 29.0.1 |

```
cargo-audit runs in CI (non-blocking advisory check).
```
