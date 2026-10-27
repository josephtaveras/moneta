# Moneta Product Spec

## Product

Moneta is a single-owner, local-first finance data hub for an AI agent.
It imports USD accounts, balances, and posted transactions from SimpleFIN into SQLite and exposes deterministic CLI reports.
Go, direct SQL, integer cents, encrypted provider credentials, and the existing TOON/JSON boundary remain the foundation.

## Current State

The SimpleFIN cutover is complete and merged to `main` (PR #35, 2026-10-27).
Imported data and core reports reconcile over representative statement periods, and remaining coverage gaps are documented.
Additional statements are optional follow-up.
Unverified account balances and unresolved classifications must not be presented as fully reconciled.
Feature count and completion of the historical build phases are not evidence of financial accuracy.
No follow-on milestone is authorized yet; each one needs explicit maintainer authorization and stated acceptance criteria.
The cutover decisions, acceptance checklist, and deferred work live in [simplefin-plan.md](simplefin-plan.md).
The historical architecture and delivered phases remain in [moneta-plan.md](moneta-plan.md).

## Delivered MVP

1. Claim a SimpleFIN setup token locally and persist only the encrypted Access URL.
2. Discover accounts and explicitly configure their types and balance signs.
3. Import posted transactions with scoped identities, overlapping date windows, and atomic checkpoints.
4. Expose incomplete imports and authentication failures without advancing checkpoints or presenting them as healthy.
5. Preserve local category, transfer, and exclusion corrections through repeat imports.
6. Reconcile accounts, transactions, spending, cashflow, and net worth against real statements.

## Scope Boundaries

- Single owner, USD only, account-level balances rather than investment positions.
- SimpleFIN is the only active provider; historical Plaid data remains readable, with no automatic merge into SimpleFIN accounts.
- No new analytics, A2 taxonomy, generic rule engine, provider registry, web UI, or REST write API without an authorized milestone.
- Existing REST reads and advanced analytics remain available, but receive no feature expansion without an authorized milestone.
- SimpleFIN sync does not run recurring detection until the unresolved classifications are reviewed on live data.
- Card terms and categories unavailable from the provider remain unknown; names and signs must not be guessed.
- Personal/business routing, CSV imports, budgets, and unused schema provisions are deferred.
- No new dependencies, telemetry, multi-user hosting, or background scheduling framework.

## Privacy and Operations

Credentials enter commands through environment variables, never command-line arguments or chat.
Access URLs are encrypted with AES-GCM using MONETA_ENCRYPTION_KEY.
Private configuration, exports, and reconciliation evidence stay in gitignored .local/ or the local database.
Provider HTTP errors never expose credential-bearing URLs or raw response bodies.
Bank data is read-only at SimpleFIN; local corrections modify only Moneta.

## Cutover Acceptance

Every item below was met when the cutover merged.

Synthetic integration tests cover identity collisions, repeated imports, corrections, failed/partial requests, and liability signs.
A live connection is configured and core reports reconcile over a stated statement period.
Repeat sync preserves row counts and corrections while accepting provider revisions.
Any unavailable history, stale balances, unknown categories, or unsupported fields are recorded as limitations.
The Plaid adapter and SDK are removed; existing databases and public migration history are preserved.
