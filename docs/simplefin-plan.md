# SimpleFIN Cutover and Reconciliation

Status: SimpleFIN cutover complete under representative statement validation; the unused Plaid adapter and SDK are removed.
Synthetic HTTPS integration tests, live repeat-sync checks, build, vet, formatting, staticcheck, golangci-lint, CGO-free tests, race tests, and benchmark smoke checks pass.
Live owner-account configuration, initial import, and repeat-sync row stability are verified.
Owner-confirmed transfer corrections survived a full live history replay.
Independent statement checks pass for the supplied subset, including transaction amounts, closing balances, and CLI report arithmetic under existing exclusions.
Remaining balance-statement coverage is optional follow-up, not a build or cutover blocker.
The previously observed live balance-versus-posted-activity discrepancy resolved on a later sync.
Ambiguous classifications remain documented follow-up items.
Transaction-only exports verify supplied rows but do not replace independent opening and closing balances.
Private reconciliation evidence is stored under `.local/reconciliation/`.
Authorized September 22, 2026 following the codebase review.

## Objective

Make core financial reports trustworthy using SimpleFIN before expanding the product.
Keep the existing binary, SQLite schema, atomic ingestion, nullable money, and CLI output formats.
Avoid a rewrite, provider framework, rules engine, or new dependencies.

## Delivery Sequence

1. Implement HTTPS setup-token claim, encrypted credential persistence, and account discovery.
2. Require explicit per-account type and balance-sign configuration before import.
3. Implement posted-only sync, five-day overlap, bounded 45-day requests, scoped IDs, and atomic checkpoints.
4. Reject responses carrying provider errors and persist unhealthy status without advancing the checkpoint.
5. Add explicit local transaction corrections that survive overlapping imports.
6. Validate the complete workflow with fake HTTPS fixtures and CLI reports.
7. Connect real accounts privately, confirm signs and types, and reconcile a statement period.
8. Recheck the known feed discrepancy, document any remaining limitation, and remove the unused Plaid code and SDK with regression checks.

## Decisions

SimpleFIN is the only active provider for `link` and `sync`.
`--provider simplefin` remains accepted; other provider selections fail before opening the database.
Historical provider rows and migrations are preserved without an automatic cross-provider merge.
One named SimpleFIN connection represents one Access URL, which can contain multiple institutions.
Account and transaction identifiers are scoped by that named connection and provider connection/account IDs.
Names are display labels, never deduplication keys.
Renaming the local connection is not a supported identity migration.
Start with posted transactions only and preserve all historical rows when they disappear from a response window.
Initial sync retrieves up to 90 days in two requests of at most 45 days; subsequent sync resumes from the successful checkpoint with five days of overlap.
Live Bridge responses warn above 45 days despite the published guide allowing 90-day requests.
All windows must validate before the batch and checkpoint commit together.
Long gaps are caught up in one invocation by committing bounded 90-day passes in order, up to 10 passes; a longer gap keeps that progress and asks for another run.
An explicit reset replays the latest 90 days; it is not a promise of full institution history.
Use provider balance timestamps, not fetch time, for balance snapshot dates.
Require explicit account types and sign multipliers, including an explicit ignore choice for accounts outside scope.
Do not infer debt type from account name or silently assume every unknown account is an asset.
Use integer decimal parsing and preserve missing optional amounts as null.
An amount with fractional cents skips only its own transaction or balance record and is counted as skipped.
Reject incomplete account sets atomically, including HTTP 200 responses with structured `errlist` errors.
Legacy `errors` entries are informational connection messages; sync counts them as warnings and never prints their text.
No automatic retry loop consumes request quota.
Keep credential-bearing URLs and raw provider errors out of logs.
Local category, transfer, and exclusion overrides take precedence over provider values.
Do not automatically match transfers by equal amounts; users explicitly confirm both legs.
SimpleFIN recurring detection stays paused until the classification and history quality have been validated.

## Validation Gate

Proceed using the representative statement periods already reconciled.
Additional statements are optional; accounts without balance evidence remain explicitly unverified.
This acceptance scope permits finishing the SimpleFIN build and Plaid cleanup without asserting full-portfolio reconciliation.
A provider balance can update before posted activity; track discrepancies without fabricating transactions or changing classifications to force agreement.

- [x] Setup claim and Access URL storage succeed without plaintext credential output.
- [x] Account discovery and explicit mapping work through the CLI.
- [x] Same transaction ID in different accounts remains distinct.
- [x] Re-import is idempotent and provider revisions update the same transaction.
- [x] Category/transfer/exclusion corrections survive re-import.
- [x] A card purchase and its payment count as spending only once after correction.
- [x] Liability balances and credits produce correct signed net worth.
- [x] Partial responses, authentication errors, and failed requests preserve checkpoints; invalid amounts skip only their own record.
- [x] Synthetic integration suite, build, vet, CGO-free tests, and race tests pass.
- [x] Live accounts are linked and configured with owner-confirmed types and balance signs.
- [x] Initial live import and repeated incremental sync preserve transaction identities and financial values.
- [x] Core reports reconcile against supplied statement periods under the saved exclusions.
- [x] Record the statement coverage and unresolved classification limits; defer missing statement evidence without blocking delivery.
- [x] Recheck the known live feed discrepancy and record its resolution or remaining limitation.
- [x] Owner-confirmed transfer corrections survive a full history replay.
- [x] Remove the unused Plaid adapter and SDK, preserving existing data and migration history, and pass regression checks.

## Private Onboarding

Run `./scripts/connect-simplefin.sh` in a local terminal and paste the setup token at the hidden prompt.
The script creates a separate gitignored database and a mode-600 environment file with its encryption key, then links and discovers accounts without importing transactions.
Do not paste credentials or account output into chat.
After linking, the agent can use the private local database and environment to inspect account configuration, with the owner confirming types and liability signs before import.
The encryption key needs a separate password-manager backup.

## Deferred Work

Existing REST and advanced analytics are frozen for feature expansion.
Shared response builders may replace duplication when a touched command requires it, without a generic presentation framework.
Unused entity rules, budget tables, and materialized net-worth provisions are not activated.
Do not rewrite historical migrations to remove unused structures.
Account-level personal/business ownership, historical backfill beyond the initial window, automatic categorization, scheduling, pending transaction reconciliation, and account closure handling need separately stated acceptance criteria.
No A2 taxonomy, new analytics, UI, or SaaS work belongs to this milestone.

## Sources

- [SimpleFIN protocol](https://www.simplefin.org/protocol.html), currently a v2 draft; request version 2 explicitly and validate against Bridge.
- [SimpleFIN Bridge developer guide](https://beta-bridge.simplefin.org/info/developers), including date overlap, 90-day windows, and request limits.
