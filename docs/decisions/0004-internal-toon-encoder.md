# ADR 0004: Internal TOON encoder package

Date: 2026-07-16
Status: accepted

## Context

AXI prescribes TOON (Token-Oriented Object Notation) on stdout for ~40% token savings over JSON.
Moneta only ever encodes TOON; it never parses it.
No mature, official TOON library is assumed to exist for Go, and betting a core output path on a young third-party package is a maintenance risk.

## Decision

Implement a small internal `internal/toon` package covering the spec subset Moneta emits: scalars, nested objects, and `[N]{fields}:` tabular arrays with spec-conformant quoting.
Validate with golden-file tests against examples from the official TOON spec.
Internal logic stays on Go structs; TOON conversion happens only at the stdout boundary.

## Consequences

- Zero dependency risk on the primary agent-facing output path.
- The encoder is small (roughly 100-200 lines) because encode-only avoids the hard half of the spec.
- If an official Go TOON library matures, swapping it in is a contained change behind one package boundary.

## Verification (2026-08-28)

The "~40% token savings" in the Context above was AXI's published claim, carried over unmeasured.
It is now measured on Moneta's own output, using a 500-transaction `moneta tx --full` payload from the production document builder (`cmd/moneta/testdata/`, `scripts/token_compare.py`, tiktoken 0.14.0):

| Baseline | Token reduction (`o200k_base`) | Token reduction (`cl100k_base`) |
| --- | --- | --- |
| Compact JSON, what `--json` emits | 29.4% | 26.6% |
| Indented JSON, typical REST shape | 55.6% | 55.2% |

The decision stands, and the encoder is doing what it was chosen to do.
The AXI headline figure is only reached against pretty-printed JSON; against Moneta's own compact `--json` escape hatch the real saving is roughly 27-29%.
Quote the compact-JSON number in anything user-facing, since that is the honest comparison for this codebase.
Byte reduction on the same payload is 49.1%, and `TestTOONPayloadSmallerThanJSON` enforces a 40% floor on it in CI so this does not silently regress.
