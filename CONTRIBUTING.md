# Contributing to metrics

Notes specific to this repo. For org-wide defaults (workflow, commit
conventions), see the linked policies at the bottom.

## What this library is

A hand-rolled Prometheus / OpenMetrics text-exposition library with **zero
dependencies** — standard library only (`go.mod` declares no `require` block).
Keep it that way: a new third-party import is a design change, not a routine
addition. The package is flat (one Go package at the repo root); there are no
subpackages or `cmd/` binaries.

## Architecture you need to know before editing

The public surface is documented in `README.md`; the load-bearing internals are
not, and they are easy to break:

- **One neutral IR, two thin per-format encoders.** A scrape is materialised
  once into a format-neutral intermediate representation — `[]metricFamily`,
  each family carrying its name, type, HELP, and a slice of `sample`s
  (`exposition.go`). Every metric type has a `family()` snapshot method
  (`exposition.go`) that reuses the shared model (`Histogram.snapshot`,
  `sortedLabelKeys`, `buildLabelString`, `collectProcessMetrics`); the registry
  walks its six metric slices plus process metrics once in `Registry.collect`.
  Two thin encoders then render that IR: `encodePrometheus` (text `0.0.4`) and
  `encodeOpenMetrics` (`1.0.0`), each owning ONLY the genuinely format-specific
  bits — HELP/TYPE line order (OpenMetrics emits `# TYPE` before `# HELP`), HELP
  escaping (`omHelpEscaper` also escapes `"`, `helpEscaper` does not), the
  counter `_total` suffix (`omCounterBaseName`/`omCounterSampleName` — base name
  on the family lines, `_total` on the sample series), and the mandatory
  trailing `# EOF` (OpenMetrics only). Because values and label strings are
  pre-rendered in the IR by the shared `formatValue` (`metrics.go`) and
  `buildLabelString`, both formats render numbers and labels identically:
  whole finite values as bare integers (e.g. `42`, no `.0`), others in shortest
  round-trippable form, `+Inf`/`-Inf`/`NaN` for non-finite. A per-format float
  formatter is a bug, not a feature — keep value rendering single-sourced.
  Exposition output is byte-locked by golden fixtures (`testdata/*.golden`,
  `golden_test.go`); regenerate them with `UPDATE_GOLDEN=1 go test` only after a
  deliberate, reviewed format change. The exported `Write*` functions
  (`counter.go`, `gauge.go`, `histogram.go`, `process.go`) and the
  `writeOMSimpleCounter`/`writeOMGauge`/`writeOMLabeledGauge` helpers remain as
  thin shims over the IR + encoders, preserved for the public API and the test
  suite.
- **Label storage is a fixed-size key.** `labelKey` is `[4]string`, so a metric
  supports **at most 4 labels** — constructors panic past that. Label values
  are copied into the array and the rendered label string is always sorted by
  label name (`buildLabelString`) for deterministic output.
- **Validation and arity are fail-fast panics, by design.** Metric/label names
  are validated at construction (`validate.go`); `Inc`/`Observe`/`Set` panic on
  label-arity mismatch; `Counter.Add` panics on a negative delta; registering
  two metrics whose exposition family names collide panics (`reserveName`,
  including the pre-seeded `process_*` family names). Tests assert these
  panics — don't soften them to error returns.
- **Spec-exact escaping is non-negotiable.** `labelEscaper` escapes only `\`,
  `"`, and `\n`; `helpEscaper` escapes only `\` and `\n`. The fuzz and red-team
  tests pin this exactly — widening or narrowing the set will fail them.
- **Histogram internals.** Buckets are cumulative, the `+Inf` bucket always
  equals `_count`, and the running sum is stored as float bits updated via an
  atomic compare-and-swap loop (`Histogram.Observe`). Bounds are validated at
  construction — they must be a strictly increasing sequence of finite values,
  and non-finite, duplicate, or out-of-order bounds panic (no silent sorting).
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
conformance tests (`openmetrics_test.go`), executable README examples
(`example_readme_test.go`), and adversarial suites (`redteam*_test.go`,
`fuzz_completeness_test.go`). Run a fuzz target directly when touching parsing,
validation, or escaping:

```sh
go test -run '^$' -fuzz FuzzLabelValueExposition -fuzztime 30s
```

Available fuzz targets include `FuzzMetricNameValidation`,
`FuzzLabelNameValidation`, `FuzzHelpTextExposition`,
`FuzzLabelValueExposition`, `FuzzOpenMetricsLabelExposition`,
`FuzzRegistryFullExposition`, and `FuzzHistogramObserve`.

## Linting and formatting

CI runs `golangci-lint` (v2 config in `.golangci.yaml`). Formatting is enforced
as a lint failure, so run both before pushing:

```sh
golangci-lint run          # vet + the enabled linter set
golangci-lint fmt          # applies gofumpt (extra-rules) + gci import grouping
```

`gofumpt` runs with `extra-rules` (groups adjacent same-type params, forbids
naked returns) and `gci` orders imports as standard then third-party — match
the existing files rather than fighting the formatter.

Mutation testing is configured (`.gremlins.yaml`, run via `gremlins unleash`)
but is a non-blocking weekly signal, not a PR gate.

## A note on synced config

`.golangci.yaml`, `.gremlins.yaml`, and `.github/workflows/*.yaml` are synced
from `cplieger/ci` and carry `DO NOT EDIT` headers. Change them upstream in
`cplieger/ci`, not here — local edits get overwritten on the next sync.

## Commits and PRs

Branch from `main`, keep changes focused with tests, and open a PR. Commits
follow [Conventional Commits](https://www.conventionalcommits.org/) (parsed by
git-cliff for releases): `feat:`, `fix:`, `sec:`, `docs:`, etc. Because the two
write paths mirror each other, a single logical change often touches both
`*.go` and `openmetrics.go` — keep them in one commit.

## Conduct & security

By participating you agree to the
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report vulnerabilities via the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md) —
never in a public issue.
