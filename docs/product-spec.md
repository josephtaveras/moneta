# Moneta Product Spec

## Product

Moneta is a single-owner, local-first finance data hub for an AI agent.
It imports USD accounts, balances, and posted transactions from SimpleFIN into SQLite and exposes deterministic CLI reports.
Go, direct SQL, integer cents, encrypted provider credentials, and the existing TOON/JSON boundary remain the foundation.

## Current Priority

The acceptance criterion is that imported data and core reports reconcile over representative statement periods, with remaining coverage gaps documented.
Additional statements are optional follow-up and do not block finishing the build.
Unverified account balances and unresolved classifications must not be presented as fully reconciled.
Feature count and completion of the historical build phases are not evidence of financial accuracy.
The active scope and acceptance checklist live in [simplefin-plan.md](simplefin-plan.md).
The historical architecture and delivered phases remain in [moneta-plan.md](moneta-plan.md).

## MVP

1. Claim a SimpleFIN setup token locally and persist only the encrypted Access URL.
2. Discover accounts and explicitly configure their types and balance signs.
3. Import posted transactions with scoped identities, overlapping date windows, and atomic checkpoints.
4. Expose incomplete imports and authentication failures without advancing checkpoints or presenting them as healthy.
5. Preserve local category, transfer, and exclusion corrections through repeat imports.
6. Reconcile accounts, transactions, spending, cashflow, and net worth against real statements.

## Scope Boundaries

- Single owner, USD only, account-level balances rather than investment positions.
- SimpleFIN is the only active provider; historical Plaid data remains readable, with no automatic merge into SimpleFIN accounts.
- No new analytics, A2 taxonomy, generic rule engine, provider registry, web UI, or REST write API.
- Existing REST reads and advanced analytics remain available, but receive no feature expansion before reconciliation.
- SimpleFIN sync does not run recurring detection automatically during this validation phase.
- Card terms and categories unavailable from the provider remain unknown; names and signs must not be guessed.
- Personal/business routing, CSV imports, budgets, and unused schema provisions are deferred.
- No new dependencies, telemetry, multi-user hosting, or background scheduling framework.

## Privacy and Operations

Credentials enter commands through environment variables, never command-line arguments or chat.
Access URLs are encrypted with AES-GCM using MONETA_ENCRYPTION_KEY.
Private configuration, exports, and reconciliation evidence stay in gitignored .local/ or the local database.
Provider HTTP errors never expose credential-bearing URLs or raw response bodies.
Bank data is read-only at SimpleFIN; local corrections modify only Moneta.

## Done Means

Synthetic integration tests cover identity collisions, repeated imports, corrections, failed/partial requests, and liability signs.
A live connection is configured and core reports reconcile over a stated statement period.
Repeat sync preserves row counts and corrections while accepting provider revisions.
Any unavailable history, stale balances, unknown categories, or unsupported fields are recorded as limitations.
The Plaid adapter and SDK are removed; existing databases and public migration history are preserved.
