# Moneta

A self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.

Moneta ingests financial data from SimpleFIN, normalizes it into a canonical model in SQLite, and exposes it through a token-efficient, agent-ergonomic CLI following the [AXI](https://github.com/kunchenguid/axi) design principles with [TOON](https://github.com/toon-format/toon) output.
A small localhost REST API mirrors the same operations for non-CLI consumers.

## Status

Pre-alpha: SimpleFIN cutover is implemented and validated against representative statement periods.
`moneta link` and `moneta sync` use SimpleFIN only; the Plaid adapter and SDK have been removed.
The SimpleFIN path includes encrypted setup-token exchange, explicit account configuration, posted-only overlapping sync, and durable local transaction corrections.
Synthetic HTTPS integration tests and live statement checks cover the workflow.
Missing statement coverage and unreviewed classifications remain explicit limitations; additional statements are optional.
Existing CLI/REST reads and Phase 3-4 analytics are retained, with feature expansion frozen until core reports reconcile.
SimpleFIN sync leaves recurring detection paused, so inferred bills are unavailable until the data-quality gate is cleared.
Upgrading resets detector state left by earlier Plaid detection to `never_run`, so stale detected rows stay hidden in bills, recurring, and the dashboard.
See the active [scope and acceptance checklist](docs/simplefin-plan.md) and [product spec](docs/product-spec.md).

## Principles

- Local-first: your data stays on your machine, except outbound calls through the selected financial-data provider adapter.
- Agent-first: pre-computed aggregates, compact TOON output, grep-friendly lines, definitive empty states, no interactive prompts.
  TOON costs 29.4% fewer tokens than the equivalent compact JSON on a 500-transaction payload; see [Benchmarks](#benchmarks).
- Harness-agnostic: any AI agent that can run shell commands (or call the REST API) can consume Moneta; no agent-framework lock-in.
- Pluggable providers: no provider-specific fields leak into the core model; imports are idempotent with transaction-level dedup.
- Single static Go binary, CGO-free, minimal dependencies, no telemetry.

## Current build scope

1. Connect SimpleFIN and confirm account types and balance signs.
2. Import posted data with stable identities and conservative failure handling.
3. Correct transfers and categories locally without losing changes on re-import.
4. Reconcile representative statement periods and document remaining coverage and classification limits.

New analytics, UI, provider infrastructure, and multi-entity routing are deferred.
The earlier feature phases are a historical implementation record, not a financial correctness guarantee.

## Development context

Agents and contributors: start with [AGENTS.md](AGENTS.md), then [docs/product-spec.md](docs/product-spec.md) for scope and [docs/decisions/](docs/decisions/) for settled choices.

The module pins its Go toolchain in `go.mod`.
Run the current schema and canonical-contract checks with:

```sh
go test ./...
```

CI runs on pull requests and pushes to `main`: build, vet, tests, CGO-free tests, and race tests.
CI runs build, vet, formatting, tests, CGO-free tests, race checks, and benchmark smoke checks.

## SimpleFIN setup

Run this from the repository root in your own terminal:

```sh
./scripts/connect-simplefin.sh
```

The script builds `.local/moneta`, creates a private `.local/simplefin.env` key/config file, and uses a separate `.local/simplefin.db` so existing Plaid data is not double-counted.
It asks for your setup token through a hidden terminal prompt, passes it through `SIMPLEFIN_SETUP_TOKEN`, claims it once, and stores the resulting Access URL encrypted in SQLite.
The setup token is neither saved nor printed.
Existing linked credentials and encryption keys are reused on subsequent runs.
Do not paste tokens or keys into chat or put them in command-line arguments.
Back up the encryption key separately in your password manager; losing it prevents credential decryption.
The private environment file is plaintext and must stay local with mode 600; database-only encryption does not protect against someone who can read that file too.
The script requires Go and OpenSSL; Moneta itself has no new runtime dependency.

For subsequent commands, load the local configuration into your terminal:

```sh
source .local/simplefin.env
.local/moneta simplefin-accounts --cached
```

Discovery displays an opaque account ID, name, institution, currency, source balance, provider balance date, and saved configuration.
Without `--cached`, discovery makes one provider request and requires the encryption key.
Configure every discovered account before the first sync:

```sh
.local/moneta configure-account --account ACCOUNT_ID --type checking --balance-sign 1
.local/moneta configure-account --account CARD_ID --type credit_card --balance-sign -1
.local/moneta configure-account --account UNUSED_ID --ignore
.local/moneta sync
```

The card example assumes the provider represents owed debt as a negative balance; inspect the actual source balance and statement before choosing a sign.
`--balance-sign` multiplies the source current balance; assets retain their normal sign, while debt owed must become positive and an overpayment must become negative.
Types are `checking`, `savings`, `credit_card`, `loan`, `investment`, and `asset`.
Non-USD accounts must be explicitly ignored.
An imported account's type, sign, or ignore decision cannot be changed silently; that requires a reviewed data migration.
`--item NAME` selects a named local connection and must be used consistently for link, discovery, configuration, and sync; the default is `default`.
A blank or whitespace-only `--item` is a usage error (exit 2) in every SimpleFIN command.
Do not reconnect the same accounts under a different name, which creates a distinct import identity.
Do not import the same real accounts through both Plaid and SimpleFIN into one database.

## SimpleFIN syncing and limits

Initial sync requests the latest 90 days in two requests of at most 45 days, subject to institution history availability.
Subsequent sync overlaps the previous checkpoint by five days and retrieves at most 90 days per pass, with each request limited to 45 days.
After a long outage, one sync invocation commits one pass at a time and continues from each committed checkpoint until it is caught up.
Each pass prints its own line on stdout as it commits, with its account, posted record, skipped, and connection warning counts and its new checkpoint.
One invocation runs at most 10 passes (at most 20 requests, about 850 days of catch-up) to stay within Bridge's daily request guidance.
If the checkpoint is still behind after that, the committed passes are kept and sync exits 1.
Run sync again the next day, after the daily request budget resets, rather than immediately.
There are no automatic retries.
`sync --reset-cursor` replays the latest 90 days while preserving the stored checkpoint until commit; it is not an all-history backfill.
Only posted transactions are imported, and missing rows in a response never trigger deletion.
Same-account provider IDs update existing rows; IDs from different accounts remain distinct.
Provider timestamps are converted to UTC calendar dates by default, including historical balance dates.
UTC stays the default because statement reconciliation matched UTC dates.
Set `MONETA_TIMEZONE` to an IANA zone name such as `America/New_York` to use that calendar for sync and discovery instead; an invalid name is a usage error (exit 2).
Choose the zone once before the first sync.
Changing it after history exists shifts the dates of overlapping rows on their next re-import, while older rows keep their original dates.
SimpleFIN errors, incomplete responses, malformed transaction identities or timestamps, and unconfigured accounts fail the import without advancing its checkpoint.
A transaction or balance amount with fractional cents (or another invalid amount) is skipped on its own and counted in its pass's skipped count; the rest of the import continues.
An invalid available balance is dropped on its own and recorded as skipped while the account's current balance still imports.
When the two requests of one pass disagree about a row, the later request's result wins, so a row is never both imported and skipped.
Informational Bridge connection messages do not fail the sync.
Sync instead prints each pass's count of connection warnings to stderr, because some institutions may be stale; check the Bridge connection page for details.
The checkpoint still advances, and each import run records its warning count.
A stale institution can add older rows after it recovers, so later syncs start their five-day overlap from the last checkpoint committed without warnings instead of the stored checkpoint.
Those late rows then import automatically, without `--reset-cursor`.
That lookback reaches at most 90 days before the stored checkpoint, so an institution that keeps warning does not grow every request.
A lookback longer than one pass is committed in passes like any other catch-up.
Syncs recorded before warning counts were tracked count as warning-free.
A failed Bridge request sets connection health to `error` or `login_required`; a successful sync or account discovery restores `ok`.
Local failures such as unconfigured accounts, an invalid checkpoint, a concurrent sync, or database errors leave connection health unchanged.
Inspect the Bridge connection page for provider error details; raw responses, Bridge message text, and credential-bearing URLs are never printed.
SimpleFIN sync pauses recurring detection; category anomalies and other existing compute-on-read analytics remain provisional until classification and history are validated.
Unknown categories remain uncategorized, and unavailable card terms remain null.

The [Bridge developer guide](https://beta-bridge.simplefin.org/info/developers) expects no more than 24 requests per day, limits request windows to 90 days, and recommends date overlap.
Although the published guide allows 90-day requests, live Bridge responses warn above 45 days, so Moneta uses smaller requests and commits only after both succeed.
A daily sync is the intended starting schedule; there is no built-in scheduler.
The adapter requests protocol version 2 and requires its scoped connection identifiers.

## Transaction review and corrections

```sh
.local/moneta tx --review --full --json
.local/moneta categories
.local/moneta correct-tx --id 123 --category 4
.local/moneta correct-tx --id 124 --transfer true
.local/moneta correct-tx --id 125 --transfer true
```

Use actual IDs from your local command output; the numbers above are examples.
For a card payment or internal transfer, explicitly mark both legs when both accounts are connected.
Moneta does not guess counterparties from matching amounts.
`tx --review` supports the normal date/account/search/limit flags and adds transaction IDs, category IDs, transfer/exclusion flags, and whether classification is manual.
`--category 0` marks a row uncategorized.
`--transfer true` marks a transfer and excludes the row.
`--transfer false` removes transfer classification without changing exclusion; use `--excluded false` to include the row again.
`--excluded true|false` controls analytics inclusion; a transfer cannot be included.
Each correction preserves the entire effective classification against re-import, while provider amounts, dates, and descriptions can still update.
Edit the correction again to change it; there is no automatic return-to-provider classification command in this milestone.
Category edits use the category's transfer/ignore semantics unless explicitly overridden.
Corrections do not run recurring detection, which stays paused for SimpleFIN data.
Core reports are only as accurate as account mapping and transfer review; uncategorized card payments otherwise inflate spending.

## Existing Plaid databases

Historical accounts, transactions, encrypted credentials, and migration history are preserved and remain readable.
Plaid linking and syncing are no longer supported; `--provider plaid` returns a usage error before opening the database.
The removed Link server's `link --listen` flag is no longer accepted.
Use a separate SimpleFIN database for overlapping accounts to avoid double-counting historical Plaid data.
No automatic cross-provider account merge is performed.

## Status

After linking and syncing, inspect connection health with the same environment (`MONETA_DB_PATH`, or `--db`):

```sh
go run ./cmd/moneta status
```

`moneta status` reads only the local database and prints TOON on stdout: a summary block (item, account, and needs-attention counts), the recurring `detector` freshness object, one row per linked item with institution, stored health, account and transaction counts, and last-sync time, then a next-step hint.
The detector object contains `status`, nullable `last_run_at` and `last_success_at`, and `last_skipped_overflow`; status output also includes redacted `last_error` after detector failure.
With nothing linked it says so and points at `moneta link`.
Flags: `--json` emits compact JSON instead of TOON, and `--limit` / `--full` control row truncation (default 20).
Exit codes follow the AXI convention: 0 ok, 1 error, 2 usage, and 3 when an item reports `login_required` and needs reconnection.
Output never includes amounts, account names, or credentials.

## Accounts

```sh
go run ./cmd/moneta accounts [--type credit_card] [--json] [--limit N | --full]
```

`moneta accounts` prints the plan's four-field schema (name, type, balance, status) as a TOON table, with a summary block of total/active/per-type counts.
Balance is the latest synced snapshot in dollars, `null` when the account has none yet.
`--type` filters to one canonical type (`checking`, `savings`, `credit_card`, `loan`, `investment`, `asset`).
`--entity` is deferred: Phase 1 is single-entity, so it would be a no-op today.
Exit codes: 0 ok, 1 error, 2 usage.

## Transactions

```sh
go run ./cmd/moneta tx [--from 2026-07-01 --to 2026-07-31] [--account checking] [--search grocery] [--json] [--limit N | --full]
```

`moneta tx` prints an aggregate summary over every match (count, excluded_count, signed total, inflow, outflow in dollars), then a TOON table of date, amount, merchant, status, account, newest first, 20 rows by default with a truncation line.
The listing shows every matching row, but the money totals follow the analytics-exclusion rule and omit `excluded` rows (transfers and card payments); `excluded_count` reports how many rows the totals omitted.
`--from`/`--to` are inclusive YYYY-MM-DD dates, `--account` is a case-insensitive account-name substring, and `--search` is a case-insensitive merchant substring.
With no matches, the hint suggests widening the filters.
Exit codes: 0 ok, 1 error, 2 usage.
Deferred plan filters for later slices: `--cat`, `--merchant`, `--entity`, `--min`/`--max`.

## Spending

```sh
go run ./cmd/moneta spend [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json] [--limit N | --full]
```

With no period flags, `moneta spend` uses the current calendar month in the host's local timezone.
`--period` accepts a calendar month in YYYY-MM form; custom `--from` / `--to` dates are inclusive and must be supplied together.
`--account` is a case-insensitive literal account-name substring.

The summary reports period bounds, posted spending transaction count, and positive `total_spend` in dollars.
Spend includes posted outflows only and always applies `excluded = 0`, so pending rows, transfers, card payments, and inflows do not affect the totals or breakdowns.
Source outflows remain negative cents in SQLite; the spend command deliberately presents them as positive spend.
Category and merchant tables are ordered by spend, use an `Uncategorized` bucket when needed, and show 20 groups each by default with independent truncation lines.
Exit codes: 0 ok, 1 error, 2 usage.

## Trends

```sh
go run ./cmd/moneta trends --metric mom [--period 2026-07] [--account checking] [--json] [--limit N | --full]
go run ./cmd/moneta trends --metric merchants [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json] [--limit N | --full]
go run ./cmd/moneta trends --metric utilization [--history 30d | --period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account card] [--json]
go run ./cmd/moneta trends --metric savings [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json]
go run ./cmd/moneta trends --metric fixed-variable [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json]
```

`--metric` is required, and the supported values are `mom`, `merchants`, `utilization`, `savings`, and `fixed-variable`; later trend metrics return a usage error until their own PRs land.
For `mom`, the selected calendar month is the current comparison period, with the immediately preceding calendar month as its baseline.
With no `--period`, `mom` uses the current month in the host's local timezone.
Custom `--from` / `--to` windows are rejected for `mom` so both sides remain calendar months.

For every category present in either month, `mom` reports `spend_this` and `spend_prev` as positive magnitudes of posted, non-excluded outflows, and signed `delta` as `spend_this - spend_prev`.
Categories retain `category_id` identity even when display names match; uncategorized rows share the `Uncategorized` bucket.
Rows sort by absolute delta descending, then category name and ID.

The `merchants` metric uses the current local calendar month by default, an explicit `--period YYYY-MM`, or a complete inclusive `--from` / `--to` pair with the same validation rules as `moneta spend`.
It groups by exact `merchant_norm`, so multiple raw spellings with the same normalized merchant remain one row.
An empty normalized key uses one `Unknown Merchant` bucket rather than splitting by raw spelling.
Rows sort by spend descending, then merchant name and normalized key.

The `utilization` metric returns one carried-forward portfolio point for every calendar day.
It defaults to the 30 local-calendar days ending today, inclusive, and also accepts `--history Nd`, `--period YYYY-MM`, or a complete inclusive `--from` / `--to` pair.
Those window forms cannot be combined.
Explicit `--limit` / `--full` are rejected because utilization has no row-limited per-card table.

Utilization includes credit-card accounts only.
For each card and day, it uses the latest balance snapshot on or before that day.
A non-NULL snapshot `limit_cents` is authoritative; only a NULL snapshot limit falls back to the current `credit_terms.limit_cents`.
Cards without a positive usable limit are excluded from that day's portfolio, and utilization is `null` when no card remains.
A positive balance contributes debt, while a zero or negative credit balance contributes zero debt when the card has a positive limit.
Portfolio utilization is total debt divided by total limits through integer-only `cli.Ratio(..., 4)` arithmetic.
The summary `accounts` count covers matched credit cards, each point's `accounts` covers included cards, and `missing_limit_days` counts days when at least one carried card balance lacks a positive usable limit.
Historical limit values stored as zero before nullable-money PR2 remain indistinguishable from real reported zero and are excluded rather than backfilled or treated as 0% utilization.

The `savings` metric uses the same current-month default, explicit calendar month, or complete inclusive custom date pair as `moneta cashflow` and `merchants`.
It calls the same `store.ReadCashflow` aggregation as `moneta cashflow`, so count, inflow, positive outflow magnitude, signed net, and savings rate cannot use a separate formula.
Savings rate is `cli.Ratio(net, inflow, 4)` and is `null` when inflow is zero.
The metric is summary-only, so `--history`, `--limit`, and `--full` are rejected.

The `fixed-variable` metric is heuristic v1, not recurring detection or a user-editable taxonomy.
It uses the same current-month default, explicit calendar month, or complete inclusive custom date pair as `moneta spend`.
It applies the exact spend filters: posted, `excluded = 0`, and negative amounts only.
A row is fixed when its category name is exactly `Rent and Utilities`; matching is by name only and does not depend on seeded category IDs.
A row with a NULL `category_id` is unclassified, and every other categorized row that passes the spend filters is variable.
The summary reports positive fixed, variable, unclassified, and total spend magnitudes plus `fixed_share = cli.Ratio(fixed, total, 4)`; the share is `null` when total is zero.
The fixed three-row `by_bucket` table also reports transaction counts, so `--history`, `--limit`, and `--full` are rejected.

The `mom`, `merchants`, `savings`, and `fixed-variable` transaction metrics include posted rows only and always apply `excluded = 0`.
The `mom`, `merchants`, and `fixed-variable` metrics include outflows only, while `savings` includes both inflows and outflows.
The row-based `mom` and `merchants` metrics truncate to 20 rows by default without changing summary totals.
All five metrics use the same case-insensitive escaped literal `--account` substring behavior.
Exit codes: 0 ok, 1 error, 2 usage.

## Cash flow

```sh
go run ./cmd/moneta cashflow [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json]
```

Cash flow uses the same period and account-filter contract as spend: current local calendar month by default, or an explicit YYYY-MM month / inclusive custom date pair.
It includes posted rows with `excluded = 0`; refunds and other positive rows count as inflow, while negative rows are presented as positive outflow magnitude.
The summary reports count, inflow, outflow, signed net (`inflow - outflow`), and `savings_rate`.
Savings rate is `net / inflow`, truncated toward zero to four decimal places (`0.1234` means 12.34%); it is `null` when inflow is zero.
Money remains integer cents internally and renders through `cli.Money`; rate construction uses integer arithmetic, never float64.
Exit codes: 0 ok, 1 error, 2 usage.

## Net worth

```sh
go run ./cmd/moneta networth [--as-of 2026-07-22 | --history 90d] [--json]
```

By default, net worth uses each account's latest available balance and reports the newest selected balance date as `as_of`; it is `null` when no balance snapshots exist.
`--as-of` selects each account's latest balance on or before the inclusive YYYY-MM-DD cutoff and echoes that requested date in the summary.
`--history Nd` returns exactly N daily points ending on today's local-calendar date, inclusive; the start is the end minus N-1 days, so `90d` returns 90 points.
History carries each account's latest balance forward across days without a new snapshot and supports 1 through 3660 days.
`--history` and `--as-of` cannot be combined.
Checking, savings, investment, and asset accounts contribute to assets.
Credit-card and loan balances use the canonical provider-boundary sign: positive means owed and negative means the institution owes the user.
Signed net worth is `assets - liabilities`, so a negative liability credit raises net worth.
Accounts without an eligible snapshot are counted in `missing_balance` and omitted from every money total; a by-type row with no eligible balances renders `balance: null` rather than inventing zero.
All stored accounts participate, including inactive accounts, because the current schema does not track historical active intervals.
Money remains integer cents internally and renders through `cli.Money`.
Exit codes: 0 ok, 1 error, 2 usage.

## Debts

```sh
go run ./cmd/moneta debts [--json]
```

Debts lists every credit-card and loan account using its latest balance snapshot and preserves the canonical balance sign.
A positive balance is owed debt, while a negative balance is a genuine credit owed to the user.
An account without a snapshot remains in the table with `balance: null`, increments `missing_balance`, and contributes nothing to `total_debt`.
Credit-card limit, APR, and due day and loan APR are best-effort values from the existing terms tables; unavailable fields are `null`.
Provider-omitted optional money remains SQL `NULL`, while an explicitly reported `0.00` remains distinguishable as zero.
Utilization is `balance / limit`, truncated toward zero to four decimal places, and is `null` when balance is missing or limit is absent, zero, or negative.
Stored APR uses percentage points; output converts it to a decimal fraction rounded to the nearest basis point, so stored `22.99` renders as `0.2299`.
Money remains integer cents internally and renders through `cli.Money`.
Exit codes: 0 ok, 1 error, 2 usage.

## Cards

```sh
go run ./cmd/moneta cards [--json]
```

Cards is the credit-card-only view of the same balance and terms data behind `moneta debts`: balance, limit, utilization, APR, and due day.
Loan accounts are not included and stay under `moneta debts`; every other account type is excluded from both.
The table omits the `type` column because every row is a credit card, and the summary keeps the `count` / `total_debt` / `missing_balance` shape so it lines up with `debts`.
Both commands read one shared store query, so balance-sign, terms, and ordering behavior is identical.
Utilization is `balance / limit`, truncated toward zero to four decimal places, and is `null` when the balance is missing or the limit is absent, zero, or negative; a missing limit never renders as `0`.
Balances keep their canonical sign, so an overpaid card reports a negative balance and reduces `total_debt` honestly.
A card without a balance snapshot stays in the table with `balance: null` and increments `missing_balance`.
Money remains integer cents internally and renders through `cli.Money`, and APR renders as a decimal fraction rounded to the nearest basis point.
Exit codes: 0 ok, 1 error, 2 usage.

## Recurring

List detected and manual recurring series after sync:

```sh
go run ./cmd/moneta recurring
go run ./cmd/moneta recurring --kind subscription
go run ./cmd/moneta recurring --json
```

Rows contain `name`, `kind`, `cadence`, signed `expected`, nullable `next_expected_date`, exact `drift_pct`, `drift`, `active`, and `source`.
The summary includes the four-field detector freshness object, active detected counts by kind, signed monthly-equivalent subscription and bill value, and the count of noncanonical cadences left unconverted.
Manual rows are always listed when they match `--kind`, but never enter detected kind counts or monthly-equivalent totals.
When detector status is `never_run` or `error`, detected rows are omitted while manual rows remain available.
Status `partial` includes the conservative detected snapshot and projects active next dates at read time without changing stored lifecycle evidence.
Drift is computed from integer cents with arbitrary-precision arithmetic and is flagged only when magnitude differs by more than 10 percent.
Exit codes: 0 ok, 1 error, 2 usage.

## Bills

List detected obligations and active credit-card dues over an inclusive calendar horizon:

```sh
go run ./cmd/moneta bills
go run ./cmd/moneta bills --days 60
go run ./cmd/moneta bills --json
```

The default horizon is today through today plus 30 calendar days; `--days` accepts 1 through 366.
Rows contain `date`, `name`, nullable non-negative `amount`, `source`, `kind`, `date_source`, and `due_status`.
Detected subscriptions and bills appear only when detector status is `ok` or `partial`.
Under `partial`, stale active schedules are projected forward at read time without mutating lifecycle evidence.
Detector status `never_run` or `error` omits detected obligations, but active card dues remain available.
Provider-reported full card dates are preferred; past dates project forward EOM-safely, and `due_day` is used only as an estimate when no full date exists.
Due dates remain visible during their one-day weekly/biweekly or three-day monthly/quarterly/card grace period.
The summary always includes the same four-field detector freshness object used by recurring, status, and dashboard.
Exit codes: 0 ok, 1 error, 2 usage.

## Anomalies

Find category-level spend spikes against the prior three months:

```sh
go run ./cmd/moneta anomalies
go run ./cmd/moneta anomalies --period 2026-04
go run ./cmd/moneta anomalies --json
```

The default period is the previous complete calendar month in the host's local timezone.
An explicitly requested current month compares month-to-date spend with equal day slices from the three preceding months; future periods are rejected.
The read includes posted, non-excluded, non-transfer outflows and groups by canonical `category_id`, including `Uncategorized`.
A category is eligible when at least two baseline months have spend and their three-month mean is positive.
It is flagged only when spend exceeds twice baseline and the increase is at least $50.
`deviation_ratio` is the fractional increase over baseline, so `1.5` means 150 percent above baseline.
The summary always includes `skipped_overflow`; a category that exceeds safe integer math is skipped without aborting the read.
Exit codes: 0 ok, 1 error, 2 usage.

## Dashboard

```sh
go run ./cmd/moneta dashboard [--json]
```

Dashboard is the content-first summary an agent reads first.
It composes the existing reads into one document and computes no new metric of its own.

```
summary:
  as_of: 2026-07-22
networth:
  assets: 10700
  liabilities: 8550
  networth: 2150
  accounts: 6
  missing_balance: 0
cash:
  balance: 1700
  accounts: 2
  note: checking + savings latest balances
credit:
  utilization: 0.34
  total_debt: 3550
  cards: 2
spend_month:
  from: 2026-07-01
  to: 2026-07-31
  total: 25
  count: 1
cashflow_month:
  inflow: 1000
  outflow: 25
  net: 975
  savings_rate: 0.975
  count: 2
sync:
  items: 1
  needs_attention: 0
  login_required: 0
recurring_detect:
  status: ok
  last_run_at: 2026-07-22T12:01:00.000Z
  last_success_at: 2026-07-22T12:01:00.000Z
  last_skipped_overflow: 0
upcoming_bills:
  count: 2
  bills[2]{date,name,amount,source,kind,date_source,due_status}:
    2026-07-25,Streambox Example,18,recurring,subscription,detected_schedule,upcoming
    2026-07-28,Travel Card,32,card_due,bill,provider_reported,upcoming
anomalies:
  period: 2026-06
  count: 1
  top[1]{category,spend,baseline,deviation_ratio}:
    Food and Drink,310,100,2.1
  skipped_overflow: 0
hint: run moneta spend or moneta trends for the breakdown behind these totals
```

`moneta dashboard` is an explicit subcommand; bare `moneta` still prints usage and exits 2.
`as_of` is the latest balance date across accounts and is `null` when no account has a snapshot yet, rather than a fabricated today.
`cash` counts checking and savings latest balances only, so investments and other assets contribute to `networth` but not to `cash`.
`credit.utilization` is the card portfolio for now, sharing one store-level definition with the utilization trend: a non-NULL snapshot `limit_cents` wins over `credit_terms.limit_cents`, an overpaid (negative) balance contributes zero debt rather than reducing the portfolio, and cards without a snapshot or a positive usable limit are excluded.
It is `null` when no card qualifies, so a missing limit never reads as 0%.
`spend_month` and `cashflow_month` cover the current calendar month in the host's local timezone and reuse the same posted, non-excluded rules as `moneta spend` and `moneta cashflow`.
`sync` reports Item counts only; per-Item detail stays in `moneta status`.
`recurring_detect` reports the same four-field freshness object used by status, without the status-only error detail.

`upcoming_bills` is `null` when recurring detector status is `never_run` or `error`.
For `ok` or `partial`, it contains the same 30-day `ReadBills` projection as `moneta bills`, capped to five rows while preserving the total count; no due rows render as an honest empty table with count zero.
`anomalies` always reports the default previous complete calendar month used by `moneta anomalies`.
It preserves the full count and overflow-skip count while capping the ordered `top` table to three rows.
An empty result is an honest object with its period, count zero, an empty table, and `skipped_overflow`; `phase4_note` has been removed because both Phase 4 slots now have definitive semantics.

Exit codes: 0 ok, 1 error, 2 usage, 3 an Item needs reconnection.
Exit 3 matches `moneta status`, and the full document still renders first so scripts can both detect the state and read the payload.

## Benchmarks

Every figure below is produced by committed benchmarks and fixtures, not estimated.
Reproduce them with `go test -run '^$' -bench . -benchmem ./cmd/moneta ./internal/store`.
Absolute timings are from an Apple M4 (darwin/arm64, Go 1.26); treat the ratios as the portable result.

### Token cost versus JSON

The sample is a 500-transaction `moneta tx --full` document built by the production document builder and rendered through the same `internal/cli` output path the binary uses.
The fixtures live in `cmd/moneta/testdata/` and are regenerated with `go test ./cmd/moneta -run PayloadFixtures -update-payload-fixtures`; `TestPayloadFixtures` fails if they drift from what the code emits.

| Format | Bytes | Tokens (`o200k_base`) | Tokens (`cl100k_base`) |
| --- | --- | --- | --- |
| TOON | 27,993 | 10,834 | 10,984 |
| JSON, compact (`--json`) | 54,957 | 15,345 | 14,965 |
| JSON, indented | 80,006 | 24,392 | 24,493 |

**TOON costs 29.4% fewer tokens than compact JSON and 55.6% fewer than indented JSON on a 500-transaction payload** (`o200k_base`; 26.6% and 55.2% under `cl100k_base`).

Two caveats worth stating plainly.
The compact-JSON row is the conservative baseline: it is the smallest reasonable JSON encoding of the same document, and it is what `--json` actually emits, so the headline number is the one Moneta's own escape hatch has to beat.
The indented row is the fairer comparison against a typical REST API, which is where the larger figure comes from.
Second, `tiktoken` is an OpenAI tokenizer and Moneta's primary consumer is Claude, whose tokenizer is not public; these counts are the standard industry proxy, not an exact cost for any one model.

Token counting runs out of band, in `scripts/token_compare.py`, because a tokenizer would mean a new dependency and the core stays dependency-light and CGO-free:

```
python3 -m venv .venv && .venv/bin/pip install tiktoken
.venv/bin/python scripts/token_compare.py
```

`TestTOONPayloadSmallerThanJSON` guards the claim inside the normal `go test ./...` run using bytes, which need no tokenizer: it fails if TOON ever drops below a 40% byte reduction against compact JSON (measured: 49.1%).

### Encode throughput

`BenchmarkRenderTx`, same document, both formats:

| Rows | TOON | JSON (compact) |
| --- | --- | --- |
| 50 | 24.2 us, 170 allocs | 33.6 us, 929 allocs |
| 500 | 229 us, 1,531 allocs | 316 us, 9,034 allocs |
| 2000 | 885 us, 6,040 allocs | 1.26 ms, 36,038 allocs |

TOON is both smaller and cheaper to produce here; the allocation gap is the tabular form emitting each field name once per table rather than once per row.

### Read path

`BenchmarkListTransactions` and `BenchmarkSummarizeTransactions` against a seeded 10,000-transaction SQLite database:

| Operation | Time |
| --- | --- |
| `ListTransactions`, default 20-row window | 1.75 ms |
| `ListTransactions`, 500-row page | 2.16 ms |
| `ListTransactions`, `--full` (10,000 rows) | 8.81 ms |
| `SummarizeTransactions`, unfiltered | 2.81 ms |
| `SummarizeTransactions`, one month | 0.68 ms |
| `SummarizeTransactions`, merchant search | 3.67 ms |

**Known limitation, surfaced by these benchmarks.** The 20-row read costs nearly as much as the 500-row read because it is not index-assisted.
`transactionFilterWhere` uses the `(? = '' OR column >= ?)` optional-filter idiom, which is not sargable, so `EXPLAIN QUERY PLAN` reports `SCAN transactions` plus `USE TEMP B-TREE FOR ORDER BY`: every read sorts the full matching set before applying `LIMIT`, leaving `transactions_entity_date_idx` unused.
Cost is linear in database size rather than in rows returned.
At 10k transactions this is a few milliseconds and invisible; it is the first thing to fix if the corpus grows by an order of magnitude.

## Read-only REST API

Set the database path and an API key through the environment, then start the loopback server:

```sh
export MONETA_DB_PATH="$HOME/.local/share/moneta/moneta.db"
export MONETA_API_KEY="replace-with-a-long-random-key"
go run ./cmd/moneta serve
```

The default listen address is `127.0.0.1:8080`.
Every route requires `X-API-Key`, and the key is compared in constant time after fixed-length hashing.
The key is never logged; `MONETA_API_KEY` is the recommended mechanism because `--api-key` is visible to other local users through the process list.
Responses are JSON only, set `Cache-Control: no-store`, and render money as exact decimal numbers through the same `cli.Money` boundary as CLI JSON.

```sh
curl -sS \
  -H "X-API-Key: $MONETA_API_KEY" \
  "http://127.0.0.1:8080/v1/networth?as_of=2026-07-22"
```

Read routes:

| Route | Query parameters |
|---|---|
| `GET /v1/status` | `limit`, `full` |
| `GET /v1/accounts` | `type`, `limit`, `full` |
| `GET /v1/transactions` | `from`, `to`, `account`, `search`, `limit`, `full` |
| `GET /v1/spend` | `period` or `from` + `to`, `account`, `limit`, `full` |
| `GET /v1/cashflow` | `period` or `from` + `to`, `account` |
| `GET /v1/networth` | `as_of` or `history=Nd` |
| `GET /v1/debts` | none |
| `GET /v1/cards` | none |
| `GET /v1/recurring` | optional `kind=subscription\|bill\|income` |
| `GET /v1/bills` | optional `days` from 1 to 366; default 30 |
| `GET /v1/anomalies` | optional `period=YYYY-MM`; default previous complete month |
| `GET /v1/dashboard` | none |
| `GET /v1/trends` | required `metric=mom\|merchants\|utilization\|savings\|fixed-variable`; `mom`: optional `period`; `merchants`: `period` or `from` + `to`, plus `limit`/`full`; `utilization`: `history`, `period`, or `from` + `to`; `savings` and `fixed-variable`: `period` or `from` + `to`; all: `account` |

Period and date semantics match their CLI counterparts.
Use `full=true` to disable a route's default 20-row limit.
Invalid queries return JSON with status 400; missing or incorrect keys return 401 without revealing authentication details.
The database opens once at process startup, and SIGINT/SIGTERM triggers graceful shutdown.
Exit codes: 0 clean stop, 1 runtime error, 2 usage/configuration.

Non-loopback binding is refused unless both a non-loopback `--listen` address and `--allow-non-loopback` are supplied.
That opt-in logs a prominent exposure warning; Moneta does not provide TLS, public-internet CORS, or write APIs.

## Library sync path

`moneta sync` loads the named SimpleFIN connection and its account configuration once, then syncs through the provider-independent `core.SyncProviderItem` helper.
Each pass decrypts the Access URL in memory, fetches a validated batch, and clears the decrypted bytes before returning.
The ingestor commits data and the next cursor atomically using the expected cursor as a compare-and-swap expectation.
Reset changes the pull window without discarding the stored checkpoint before commit.
When a batch reports more history remaining, the next pass pulls from the cursor just committed.
Sync never deletes historical provider data when an adapter is removed.

## License

[Apache-2.0](LICENSE)
