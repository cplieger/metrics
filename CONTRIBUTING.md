# Contributing to metrics

Notes specific to this repo. For org-wide defaults (workflow, commit
conventions), see the linked policies at the bottom.

## What this library is

A hand-rolled Prometheus text-exposition library with **zero dependencies**:
standard library only (`go.mod` declares no `require` block). Keep it that
way: a new third-party import is a design change, not a routine addition. The
package is flat (one Go package at the repo root); there are no subpackages or
`cmd/` binaries.

## Architecture you need to know before editing

The public surface is documented in `README.md`; the load-bearing internals are
not, and they are easy to break:

- **One neutral IR, one thin encoder.** A scrape is materialised once into an
  intermediate representation: `[]metricFamily`, each family carrying its
  name, type, HELP, and a slice of `sample`s (`exposition.go`). Every metric
  type has a `family()` snapshot method (`exposition.go`) that reuses the
  shared model (`Histogram.snapshot`, `sortedLabelKeys`, `buildLabelString`,
  `collectProcessMetrics`); the registry walks its six metric slices plus
  process metrics once in `Registry.collect`. `encodePrometheus` (text
  `0.0.4`) then renders that IR. Values and label strings are pre-rendered in
  the IR by the shared `formatValue` (`metrics.go`) and `buildLabelString`:
  whole finite values as bare integers (e.g. `42`, no `.0`), others in
  shortest round-trippable form, `+Inf`/`-Inf`/`NaN` for non-finite. Keep
  value rendering single-sourced. Exposition output is byte-locked by a golden
  fixture (`testdata/prometheus.golden`, `golden_test.go`); regenerate it with
  `UPDATE_GOLDEN=1 go test` only after a deliberate, reviewed format change.
  The exported `Write*` functions (`counter.go`, `gauge.go`, `histogram.go`,
  `process.go`) remain as thin shims over the IR + encoder because they are
  public API.
- **Label storage is a fixed-size key.** `labelKey` is `[8]string`, so a metric
  supports **at most 8 labels**; constructors panic past that. Label values
  are copied into the array and the rendered label string is always sorted by
  label name (`buildLabelString`) for deterministic output.
- **Validation is captured at construction and surfaces at registration; the
  record-path guards stay panics.** Metric/label names and histogram bucket
  bounds are validated at construction (`validate.go`, `checkBuckets`), but a
  violation does not panic: it is captured into the metric's unexported `err`
  field (the `client_golang` `Desc.err` shape), and every captured error names
  the metric, so a `MustRegister` panic over a package-level block identifies
  which declaration failed. The record path is this library's own divergence
  from `client_golang` (upstream keeps recording and surfaces the error at
  scrape time): an errored metric records nothing — `Inc`/`Add`/`Set`/`Observe`
  no-op, emitting one `slog` warning via `warnInertOnce` (`counter.go`) on the
  first dropped record so a constructed-but-never-registered metric is not
  silently dead — and the `Write*` shims emit nothing for it. The error
  surfaces through the registration doors — `(*Registry).Register(m) error`
  returns it, `MustRegister(m ...)` panics on the first error. Name collisions
  (`reserveName`, including the pre-seeded `process_*` family names) and
  re-registration are registration errors on the same doors; a metric refused
  for a name collision rolls back and stays registrable elsewhere. Only the
  registration CAS winner may read or write a metric's `name` field
  (`register` resolves the name through a closure after the CAS): argument
  evaluation before the CAS races the winner's rename when one value is
  registered into two registries concurrently. STILL fail-fast panics, by
  design: `Inc`/`Observe`/`Set` on label-arity mismatch, `Counter.Add` on a
  negative delta (it also saturates at `math.MaxInt64` instead of wrapping).
  `NewRegistry` is NOT in that set: an invalid prefix is captured on the
  registry and reported by every `Register`/`MustRegister`, because a prefix
  that cannot make a valid name makes every metric under it invalid and the
  registration door is where this package reports construction errors. Tests
  assert both halves; don't soften the record-path panics to error returns, and
  don't let an errored metric reach the exposition. Invalid UTF-8 is deliberately in neither set: label values
  and help text are sanitized with the Unicode replacement character (U+FFFD)
  by the shared `sanitizeUTF8` engine (`validate.go`), with a one-time `slog`
  warning per newly created sanitized series (label path) and per constructor
  (help text); repeated records sanitizing onto an existing series do not
  re-warn. The library never panics on invalid UTF-8.
- **Spec-exact escaping is non-negotiable.** `labelEscaper` escapes only `\`,
  `"`, and `\n`; `helpEscaper` escapes only `\` and `\n`. The fuzz and red-team
  tests pin this exactly; widening or narrowing the set will fail them.
- **Histogram internals.** Buckets are cumulative, the `+Inf` bucket always
  equals `_count`, and the running sum is stored as float bits updated via an
  atomic compare-and-swap loop (`Histogram.Observe`). Bounds are validated at
  construction: they must be a strictly increasing sequence of finite values,
  and non-finite, duplicate, or out-of-order bounds are a captured
  construction error surfacing at registration (no silent sorting).
  Labeled counters and histograms expose `Delete(vals...)`/`Reset()` for series
  removal, matching labeled gauges; the exposition writers nil-guard a key that
  a concurrent `Delete`/`Reset` removes mid-scrape.
- **Process metrics are partly Linux-only.** `process.go` parses
  `/proc/self/{stat,status,fd,limits}`; CPU, RSS, and FD metrics are emitted
  only when those reads succeed (gated on `>= 0` / `> 0`), so output differs
  between Linux and other platforms. Account for this when adding assertions.
- **The SKIP list is a decision, not a TODO.** `README.md` and the package
  doc-comment list features intentionally omitted (Summary type, exemplars,
  push/remote-write, protobuf, native histograms, dynamic unregister, float
  counters, gzip). Don't implement these without first reopening that decision.

## Local development

Requires the Go toolchain matching `go.mod`. Everything
runs from the repo root:

```sh
go test ./...              # unit, fuzz-seed, conformance, and red-team tests
go test -race ./...        # the metrics are concurrent (atomics + RWMutex)
go vet ./...
```

Tests live beside the code they cover (standard Go layout), including
golden-file exposition locks (`golden_test.go` + `testdata/prometheus.golden`)
and an executable README example (`example_test.go`). Run a fuzz target
directly when touching parsing, validation, or escaping:

```sh
go test -run '^$' -fuzz FuzzLabeledExposition_balanced -fuzztime 30s
```

Available fuzz targets include `FuzzMetricNameValidation`,
`FuzzLabelNameValidation`, `FuzzLabelValueValidation`,
`FuzzPrometheusHelpExposition`, `FuzzLabeledExposition_balanced`,
`FuzzRegistryFullExposition`, `FuzzHistogramObserve`,
`FuzzHistogram_BucketPlacementInvariant`, `FuzzFormatValueRoundTrip`, and the
`/proc` parser targets in `process_fuzz_test.go`.

## Linting and formatting

CI runs `golangci-lint` (v2 config in `.golangci.yaml`). Formatting is enforced
as a lint failure, so run both before pushing:

```sh
golangci-lint run          # vet + the enabled linter set
golangci-lint fmt          # applies gofumpt (extra-rules) + gci import grouping
```

`gofumpt` runs with `extra-rules` (groups adjacent same-type params, forbids
naked returns) and `gci` orders imports as standard then third-party; match
the existing files rather than fighting the formatter.

Mutation testing is configured (`.gremlins.yaml`, run via `gremlins unleash`)
but is a non-blocking weekly signal, not a PR gate.

## A note on synced config

`.golangci.yaml`, `.gremlins.yaml`, and `.github/workflows/*.yaml` are synced
from `cplieger/ci` and carry `DO NOT EDIT` headers. Change them upstream in
`cplieger/ci`, not here; local edits get overwritten on the next sync.

## Commits and PRs

Branch from `main`, keep changes focused with tests, and open a PR. Commits
follow [Conventional Commits](https://www.conventionalcommits.org/) (parsed by
git-cliff for releases): `feat:`, `fix:`, `sec:`, `docs:`, etc.

## Conduct & security

By participating you agree to the
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report vulnerabilities via the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md),
never in a public issue.
