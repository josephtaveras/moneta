# ADR 0005: Neutral default taxonomy mirroring Plaid PFC primary groups

Date: 2026-07-16
Status: accepted taxonomy; automatic provider mapping applied only to the removed Plaid adapter.

## Context

Moneta needs a canonical category set that works out of the box for any open-source user, while letting each user personalize without forking.
The original provider (Plaid) delivered categories in its Personal Finance Categories (PFC) taxonomy.
Provider-specific vocabulary must not leak into the core model.

## Decision

Ship a neutral default taxonomy that mirrors Plaid PFC's ~16 primary groups, stored as seed data rows, not code.
Names are neutralized so the core is not Plaid-branded.
Users add custom categories and remap provider categories via local config and the `category_mappings` layer; personalization is data, never committed to the repo.

## Consequences

- Historical Plaid imports received categories through a 1:1 mapping, and those rows keep them.
- SimpleFIN does not guarantee categories; its imports remain uncategorized until explicitly corrected locally.
- The existing neutral taxonomy remains available for those corrections; expanding it needs an authorized milestone.
- The taxonomy is standardized and externally maintained rather than invented here.
- Custom categories and mappings stay local (database + gitignored config), which also keeps personal financial vocabulary out of the public repository.
- Future providers (e.g., a RocketMoney CSV importer) ship their own default mapping into the same canonical set, overridable by the same mechanism.
