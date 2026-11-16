# Dependency & vulnerability report

_Generated 2026-11-16T15:15:24Z by `scripts/dependency-report.sh`. Direct dependencies only;
transitive trees are pinned in the respective lock files._

## Go backend (`backend/`)

| Module | Pinned | Update available |
| --- | --- | --- |
 |  github.com/golang-jwt/jwt/v5 | v5.3.1 | up to date |
 |  github.com/oschwald/geoip2-golang | v1.13.0 | up to date |
 |  github.com/stellar/go-stellar-sdk | v0.8.0 | v0.9.0 |
 |  modernc.org/sqlite | v1.61.0 | v1.62.0 |

Vulnerability scan:
```
=== Symbol Results ===

Vulnerability #1: GO-2026-4316
    Open redirect vulnerability in the RedirectSlashes middleware in
    github.com/go-chi/chi
  More info: https://pkg.go.dev/vuln/GO-2026-4316
  Module: github.com/go-chi/chi
    Found in: github.com/go-chi/chi@v4.1.2+incompatible
    Fixed in: N/A
    Example traces found:
      #1: internal/stellarx/client.go:17:2: stellarx.init calls operations.init, which eventually calls chi.init

Your code is affected by 1 vulnerability from 1 module.
This scan found no other vulnerabilities in packages you import or modules you
require.
Use '-show verbose' for more details.
```

## Web app (`web/`, Astro + React)

| Package | Pinned | Latest |
| --- | --- | --- |
| @astrojs/react | 7.1.1 | 7.1.9 |
| astro | 7.4.2 | 7.4.12 |
| react | 19.3.1 | up to date |
| react-dom | 19.3.1 | up to date |

Vulnerability scan (build toolchain advisories are noted separately; the deployed
site is a static build that ships only the React runtime, not the dev server):
```
# npm audit report

astro  <=7.4.6
Severity: moderate
Astro: XSS via attribute injection in dynamic tag names - https://github.com/advisories/GHSA-mh32-jxcr-mh9f
Astro: Image endpoint in @astrojs/node can read files from a case-variant sibling of the output directory on case-sensitive filesystems - https://github.com/advisories/GHSA-r3h5-4585-7x3w
fix available via `npm audit fix`
node_modules/astro

1 moderate severity vulnerability

To address all issues, run:
  npm audit fix
```

## Soroban contract (`contract/`)

| Crate | Requirement |
| --- | --- |
| soroban-sdk | 29.0.1 |

```
cargo-audit runs in CI (non-blocking advisory check).
```
