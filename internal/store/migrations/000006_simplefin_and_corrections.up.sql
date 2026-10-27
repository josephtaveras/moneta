CREATE TABLE simplefin_accounts (
    provider_item_id INTEGER NOT NULL REFERENCES provider_items(id) ON DELETE CASCADE,
    account_ref TEXT NOT NULL CHECK (account_ref <> ''),
    name TEXT NOT NULL,
    institution TEXT NOT NULL DEFAULT '',
    currency TEXT NOT NULL,
    raw_balance_cents INTEGER,
    balance_date TEXT NOT NULL DEFAULT '',
    type TEXT CHECK (type IN ('checking', 'savings', 'credit_card', 'loan', 'investment', 'asset')),
    balance_sign INTEGER CHECK (balance_sign IN (-1, 1)),
    ignored INTEGER NOT NULL DEFAULT 0 CHECK (ignored IN (0, 1)),
    PRIMARY KEY (provider_item_id, account_ref)
) STRICT;
ALTER TABLE transactions ADD COLUMN classification_manual INTEGER NOT NULL DEFAULT 0 CHECK (classification_manual IN (0, 1));
