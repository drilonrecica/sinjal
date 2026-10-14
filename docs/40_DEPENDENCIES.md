# Approved Dependencies

This is the authoritative dependency allow-list (decision P0-05). An approved entry may not be in use yet: `go.yaml.in/yaml/v3` arrives with config export (M9).

Each entry answers the five questions from `AGENTS.md` §4:

1. Problem: what concrete problem does it solve?
2. Not stdlib: why is the standard library or an existing dependency not enough?
3. Cost: what does it cost at runtime and in binary size?
4. Transitive: what does it pull in?
5. Worth it: is it worth the maintenance?

Rules:

- Any dependency not on this list, including a replacement for one on it, needs a new decision task before it is added.
- Pin exact versions in `go.mod` or in the vendored file header.
- Binary-size figures are approximate. They are checked against `18_PERFORMANCE.md` when M0 and M10 are measured.

## Go modules

### `github.com/go-chi/chi/v5`: routing
1. Problem: route groups, per-group middleware and URL params for the route table (`31_HTTP_ROUTES.md`).
2. Not stdlib: `net/http.ServeMux` patterns cover matching but not middleware groups/sub-routers; replicating that is code we would own.
3. Cost: negligible (<100 KB), no init work.
4. Transitive: none.
5. Worth it: **approved**.

### `modernc.org/sqlite`: SQLite driver
1. Problem: the only database (`09_DATABASE.md`).
2. Not stdlib: no SQLite in stdlib; cgo drivers break static `CGO_ENABLED=0` builds, cross-compilation to arm64 and the `scratch` image.
3. Cost: the largest dependency, roughly 6–10 MB of binary. Accepted because it is the database.
4. Transitive: `modernc.org/libc`, `mathutil`, `memory`, `fileutil`, `golang.org/x/sys` (and their small deps).
5. Worth it: **approved**. Use only via `database/sql` with handwritten SQL.

### `github.com/a-h/templ`: templates
1. Problem: type-checked server-rendered HTML components.
2. Not stdlib: `html/template` is stringly typed with runtime errors; component composition for three themes is error-prone.
3. Cost: small runtime package; generated code is plain Go.
4. Transitive: runtime has none of note. The generator is pinned with the `go.mod` `tool` directive (`go tool templ generate`) and is not linked into the binary.
5. Worth it: **approved**. Generation workflow per `35_DEV_BUILD_WORKFLOW.md`.

### `golang.org/x/crypto`: password hashing
1. Problem: Argon2id password hashing (`13_AUTH_SECURITY.md`).
2. Not stdlib: no Argon2 in stdlib.
3. Cost: small; only `argon2` (and its `blake2b`) are linked.
4. Transitive: `golang.org/x/sys`.
5. Worth it: **approved**. Use only the `argon2` package unless a new need is approved. Secret encryption uses stdlib `crypto/aes` + `crypto/cipher` (AES-GCM).

### `golang.org/x/net`: ICMP and DNS test servers
1. Problem: ICMP echo over unprivileged/raw sockets (`06_MONITORING_ENGINE.md`, P0-04); `dns/dnsmessage` to build fake DNS servers in tests.
2. Not stdlib: no ICMP socket API in stdlib; `net` has a resolver but no message builder for test servers.
3. Cost: small; only `icmp`/`ipv4`/`ipv6` are linked in production.
4. Transitive: `x/sys`, `x/text`, `x/crypto`, `x/term` (Go-team maintained).
5. Worth it: **approved**. The production DNS monitor uses stdlib `net.Resolver` (custom `Dial` for a configured resolver). `dnsmessage` is used only in production code if `net.Resolver` turns out to be insufficient for the six v1 record types, which would need a note in this file.

### `github.com/go-webauthn/webauthn`: passkeys
1. Problem: WebAuthn registration/assertion ceremonies (`13_AUTH_SECURITY.md`).
2. Not stdlib: CBOR/COSE parsing and ceremony verification are security-critical; hand-rolling them is riskier than a maintained library.
3. Cost: moderate. Measured at M1-14: +2.3 MB of stripped binary (12.6 → 14.9 MB).
4. Transitive: `fxamacker/cbor/v2`, `golang-jwt/jwt/v5`, `google/go-tpm`, `google/uuid`, `go-viper/mapstructure/v2`, `tinylib/msgp`, `go-webauthn/x` (test-only deps are not linked).
5. Worth it: **approved**. Configure attestation preference `none`; no metadata-service (MDS) fetching, so no outbound calls.

### `rsc.io/qr`: TOTP enrolment QR code
1. Problem: render the `otpauth://` URI as a QR code during TOTP enrolment.
2. Not stdlib: no QR encoder in stdlib.
3. Cost: tiny; called only on the enrolment page.
4. Transitive: none.
5. Worth it: **approved**. Rendered server-side as an inline PNG data URI. The base32 secret and the URI are also shown for manual entry.

### `go.yaml.in/yaml/v3`: YAML config export/import
1. Problem: human-readable YAML config export/import (`15_CONFIG_BACKUP.md`).
2. Not stdlib: no YAML in stdlib.
3. Cost: small (~300 KB).
4. Transitive: none.
5. Worth it: **approved**. This is the YAML organisation's maintained continuation of `gopkg.in/yaml.v3`, which is archived. The API is identical; do not use `gopkg.in/yaml.v3`. Decode with `KnownFields(true)`.

## Not a dependency

### TOTP: hand-written on stdlib
RFC 6238 (HMAC-SHA1, 6 digits, 30 s step, ±1 step) is ~60 lines with `crypto/hmac`, `crypto/sha1`, `encoding/base32` and `encoding/binary`. It is tested against the RFC 6238 Appendix B vectors and RFC 4226 Appendix D. No TOTP library is approved.

## Dev-only tooling

Not linked into the `sinjal` binary and not present in production images.

| Tool | Purpose | How it is pinned |
|---|---|---|
| templ generator | generate `*_templ.go` | `go.mod` `tool` directive (`go tool templ`) |
| staticcheck (`honnef.co/go/tools`) | static analysis for `make lint` and CI | `go.mod` `tool` directive |
| `golang` build image | the `Dockerfile`'s build stage; the runtime stage is `scratch`, so nothing of it ships | `ARG GO_VERSION=1.27` (pin to a digest for releases, M11) |
| `github.com/chromedp/chromedp` | small browser test suite (`20_TESTING.md`) | separate `tests/browser/go.mod` only (v0.21.0, with `cdproto` and `go-json-experiment/json`) |
| `modernc.org/sqlite` (test module) | `make screenshots` writes demo history into a stopped server's database | `tests/browser/go.mod` only, at the product's version (v1.60.1) |
| syft | SBOM generation for releases (`17_MIGRATIONS_RELEASES.md`) | installed binary; the release script records its version in `dist/syft-version.txt` |

Adding another dev-only tool follows the same rule as runtime dependencies: list it here first.

## Vendored web assets

Each asset is:

- vendored with a header comment recording upstream URL, exact version and licence (JS and CSS under `web/static/`; icons in `web/templates/icons.go`; fonts, which cannot carry a comment, are recorded below)
- embedded via `embed.FS` (`web/embed.go`)
- counted against the budgets in `18_PERFORMANCE.md` (<100 KB compressed first-party JS; ≤150 KB fonts)

These are never loaded from a CDN.

| Asset | Problem | Why not hand-written | Approx. size (gzip) | Licence | Verdict |
|---|---|---|---|---|---|
| htmx 2.x | partial page interactions (`AGENTS.md` §3) | mandated by the architecture | ~17 KB | Zero-Clause BSD (0BSD) | **approved**; vendored as **2.0.11** in `web/static/js/htmx.min.js` |
| htmx SSE extension | live status updates over SSE (`32_SSE_EVENTS.md`) | integrates SSE with htmx swaps | ~3 KB | Zero-Clause BSD (0BSD) | **approved**; vendored as **htmx-ext-sse 2.2.4** in `web/static/js/htmx-ext-sse.js` (unminified upstream file, 2.8 KB gzip -9) |
| uPlot | latency/availability charts | tiny, canvas-based, fast with large series | ~20 KB JS + ~1 KB CSS | MIT | **approved**; vendored as **1.6.32** in `web/static/js/uplot.min.js` (upstream `uPlot.iife.min.js`, 22.3 KB gzip) and `web/static/css/uplot.min.css` (1 KB gzip) |
| Lucide icons (subset) | UI icons | consistent icon set | only icons used, inlined as SVG | ISC (Feather-derived icons MIT) | **approved**, used icons only, no icon font; shapes from **lucide-static 1.49.0** in `web/templates/icons.go`, licence text in `web/templates/LICENSE-lucide.txt`; a test fails on an icon nothing uses |
| Inter (variable, subset) | primary UI typeface (`04_DESIGN_SYSTEM.md`) | system stacks render differently per OS | part of ≤150 KB font budget (29.9 KB) | OFL-1.1 | **approved**; **Inter 4.1** as `web/static/fonts/inter-var.woff2`, OFL text in `OFL-inter.txt` |
| JetBrains Mono (variable, subset) | technical/mono text, Terminal theme | not installed on most systems | part of ≤150 KB font budget (15.5 KB) | OFL-1.1 | **approved**; **JetBrains Mono 2.304** as `web/static/fonts/jetbrains-mono-var.woff2`, OFL text in `OFL-jetbrains-mono.txt` |

Font provenance (M8-01). A woff2 file cannot carry a header comment, so the record is here:

- Inter 4.1: `https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip` (sha256 `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`), file `InterVariable.ttf` (sha256 `4989b125924991b90d05b2d16e0e388c48f7d5bb8b30539bbf9c755278d0ccaf`).
- JetBrains Mono 2.304: `https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip` (sha256 `6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf`), file `fonts/variable/JetBrainsMono[wght].ttf` (sha256 `662a196d58f1183bf2d77428b6d5283fe3f45161ab021bea4036bc98e5cac016`).
- Converted once with fontTools 4.66 (a one-off, not a project dev tool; owner decision 2026-10-14):

```sh
U="U+0000-00FF,U+0100-017F,U+2013-2014,U+2018-201D,U+2022,U+2026,U+2190-2193,U+2212,U+20AC,U+00A0,U+2009,U+202F"
python3 -m fontTools.varLib.instancer InterVariable.ttf wght=400:700 opsz=drop -o inter-pinned.ttf
python3 -m fontTools.varLib.instancer 'JetBrainsMono[wght].ttf' wght=400:600 -o jbm-pinned.ttf
python3 -m fontTools.subset inter-pinned.ttf --unicodes="$U" --layout-features='kern,liga,calt,tnum,ccmp,locl,mark,mkmk' \
  --flavor=woff2 --no-hinting --desubroutinize --output-file=inter-var.woff2
# no calt/liga: code ligatures would change how URLs and hosts read
python3 -m fontTools.subset jbm-pinned.ttf --unicodes="$U" --layout-features='kern,ccmp,locl,mark,mkmk' \
  --flavor=woff2 --no-hinting --desubroutinize --output-file=jetbrains-mono-var.woff2
```

No frontend framework, CSS framework runtime or component kit is approved (`AGENTS.md` §2, §4).
