#!/usr/bin/env python3
"""Count LLM tokens in Moneta's TOON output versus the same payload as JSON.

Moneta's own test suite locks the byte-level size difference (see
TestTOONPayloadSmallerThanJSON in cmd/moneta/payload_bench_test.go). This
script produces the token-level figure quoted in README.md. It lives outside
the Go build on purpose: a tokenizer would be a new dependency, and the core
stays dependency-light and CGO-free.

Usage:
    python3 -m venv .venv && .venv/bin/pip install tiktoken
    .venv/bin/python scripts/token_compare.py

The fixtures it reads are generated from the production document builder:
    go test ./cmd/moneta -run PayloadFixtures -update-payload-fixtures
"""

from __future__ import annotations

import pathlib
import sys

FIXTURES = pathlib.Path(__file__).resolve().parent.parent / "cmd" / "moneta" / "testdata"
# cl100k_base is the GPT-4 / 3.5-turbo encoding; o200k_base is the current
# GPT-4o family encoding. Neither is Claude's tokenizer, which is not
# publicly available, so these are the standard industry proxy.
ENCODINGS = ("o200k_base", "cl100k_base")
SAMPLES = (
    ("tx_sample_500.toon", "TOON"),
    ("tx_sample_500.json", "JSON (compact)"),
    ("tx_sample_500.pretty.json", "JSON (indented)"),
)


def main() -> int:
    try:
        import tiktoken
    except ImportError:
        print("tiktoken is required: pip install tiktoken", file=sys.stderr)
        return 1

    payloads = []
    for filename, label in SAMPLES:
        path = FIXTURES / filename
        if not path.exists():
            print(f"missing fixture {path}; regenerate with go test", file=sys.stderr)
            return 1
        payloads.append((label, path.read_text(encoding="utf-8")))

    for encoding_name in ENCODINGS:
        encoding = tiktoken.get_encoding(encoding_name)
        counts = [(label, len(encoding.encode(text)), len(text)) for label, text in payloads]
        print(f"\n{encoding_name}")
        print(f"  {'format':<16}{'tokens':>10}{'bytes':>10}")
        for label, tokens, size in counts:
            print(f"  {label:<16}{tokens:>10,}{size:>10,}")
        toon_tokens = counts[0][1]
        for label, baseline_tokens, _ in counts[1:]:
            reduction = 100 * (baseline_tokens - toon_tokens) / baseline_tokens
            print(
                f"  vs {label:<16}{reduction:>6.1f}% fewer tokens"
                f"  ({baseline_tokens:,} -> {toon_tokens:,})"
            )

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
