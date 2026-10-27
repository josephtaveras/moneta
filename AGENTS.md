# AGENTS.md - Moneta

## Project Context

Moneta is a self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.
Single static Go binary, SQLite, SimpleFIN ingestion, AXI-style CLI with TOON output, localhost REST mirror.
Full architecture: `docs/moneta-plan.md`.

## Current Goal

The SimpleFIN cutover is complete and merged to `main` (PR #35, 2026-10-27); `docs/simplefin-plan.md` is its record.
No follow-on milestone is authorized yet.
Begin feature work only after the maintainer authorizes a milestone with stated acceptance criteria; candidates are listed under Deferred Work in `docs/simplefin-plan.md`.
Read `docs/product-spec.md` and `docs/simplefin-plan.md` before implementation.
Real-data correctness remains the acceptance gate for any change to ingestion, classification, or reports.
SimpleFIN is the only active adapter.
SimpleFIN recurring detection stays paused until the unresolved classifications are reviewed on live data.
Keep the Go/SQLite/CLI foundation and avoid adding generic provider infrastructure or a rules framework.
Preserve local corrections across overlapping imports and treat partial provider responses conservatively.
Preserve historical provider rows and migrations, and never rewrite a published migration.

## Working Rules

- Inspect the live repository state and local instructions before making changes.
- Read this file and `docs/product-spec.md` before broad changes; check `docs/decisions/` before relitigating a settled choice.
- Preserve existing work and keep diffs focused on the requested task.
- Do not reset, discard, stage, commit, push, or deploy unrelated changes.
- Do not commit or push at all without an explicit request from the maintainer.
- Reproduce bugs through the user-facing workflow before implementing a fix when practical.
- Use existing project conventions and documented commands.
- Run focused tests, lint, and relevant end-to-end checks before handing off work.
- Report uncertainty, remaining risks, and any validation that could not be completed.
- No new dependencies without approval; the core stays CGO-free, with no third-party calls outside the active provider adapters and no telemetry.

## Privacy Rules (public repo)

- This repo is public and its history is permanent; never commit personal data: real transactions, balances, account names, institutions, custom categories, or email addresses.
- All private material lives in gitignored `.local/` or in the local database/config, never in the repo.
- Docs, examples, and test fixtures use the neutral default taxonomy and fake data only.
- Commits use the repo-local noreply git identity; verify with `git config user.email` before committing.
- Secrets (SimpleFIN setup tokens and Access URLs, legacy Plaid tokens in older databases, API keys, encryption keys) come from env vars only, are never logged, and never appear in code, fixtures, or docs.

## Important Files

- `README.md` - human-facing overview.
- `docs/product-spec.md` - product frame: MVP, current priority, non-goals.
- `docs/moneta-plan.md` - approved architecture: schema, provider interface, AXI commands, phases.
- `docs/simplefin-plan.md` - SimpleFIN cutover record: provider decisions, sync limits, acceptance checklist, and deferred work.
- `docs/decisions/` - ADRs; decisions future agents should preserve.
- `docs/phase*-plan.md` - historical hand-off plans for the shipped Phase 1-4 work; not active instructions.
- `internal/report/` - documents whose CLI and REST payloads must stay identical (the dashboard today). Add a document here instead of writing a second builder in `internal/api`; most commands still keep a per-surface builder, which is fine while the two are allowed to differ.

## Done Means

- The phase deliverable runs locally (or the limitation is documented), with focused tests passing.
- README updated for whatever the phase added (setup, commands, usage).
- Changed files summarized with any remaining risk.

## Ignore For Now

- Investment holdings/positions (balance-level tracking only in v1).
- RocketMoney CSV importer implementation (Phase 2+; no stub exists yet).
- Whole-database encryption, multi-currency, brokerages.
- Human-facing web UI (roadmap item, post-v1; agent interfaces come first).
