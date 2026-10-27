package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SimpleFINAccount struct {
	ID           string
	Name         string
	Institution  string
	Currency     string
	BalanceCents *int64
	BalanceDate  string
	Type         string
	BalanceSign  int64
	Ignored      bool
}

// SaveSimpleFINDiscovery refreshes discovery metadata without changing decisions.
func SaveSimpleFINDiscovery(ctx context.Context, db *sql.DB, itemID int64, accounts []SimpleFINAccount) error {
	if db == nil {
		return errors.New("database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range accounts {
		_, err = tx.ExecContext(ctx, `INSERT INTO simplefin_accounts(provider_item_id,account_ref,name,institution,currency,raw_balance_cents,balance_date)
   VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider_item_id,account_ref) DO UPDATE SET
   name=excluded.name,institution=excluded.institution,currency=excluded.currency,raw_balance_cents=excluded.raw_balance_cents,balance_date=excluded.balance_date`, itemID, a.ID, a.Name, a.Institution, a.Currency, a.BalanceCents, a.BalanceDate)
		if err != nil {
			return fmt.Errorf("save account discovery: %w", err)
		}
	}
	return tx.Commit()
}
func ListSimpleFINAccounts(ctx context.Context, db *sql.DB, itemID int64) ([]SimpleFINAccount, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	rows, err := db.QueryContext(ctx, `SELECT account_ref,name,institution,currency,raw_balance_cents,balance_date,COALESCE(type,''),COALESCE(balance_sign,0),ignored FROM simplefin_accounts WHERE provider_item_id=? ORDER BY name,account_ref`, itemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []SimpleFINAccount{}
	for rows.Next() {
		var a SimpleFINAccount
		var balance sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Institution, &a.Currency, &balance, &a.BalanceDate, &a.Type, &a.BalanceSign, &a.Ignored); err != nil {
			return nil, err
		}
		if balance.Valid {
			a.BalanceCents = &balance.Int64
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// ConfigureSimpleFINAccount is intentionally limited to accounts not imported
// yet. Reclassifying imported debt or reversing historical signs requires an
// explicit migration, rather than silently reinterpreting financial history.
func ConfigureSimpleFINAccount(ctx context.Context, db *sql.DB, itemID int64, ref, kind string, sign int64, ignore bool) error {
	if !ignore {
		switch kind {
		case "checking", "savings", "credit_card", "loan", "investment", "asset":
		default:
			return errors.New("invalid account type")
		}
		if sign != 1 && sign != -1 {
			return errors.New("balance sign must be 1 or -1")
		}
	}
	if db == nil {
		return errors.New("database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currency, oldType string
	var oldSign int64
	var oldIgnore bool
	err = tx.QueryRowContext(ctx, `SELECT currency,COALESCE(type,''),COALESCE(balance_sign,0),ignored FROM simplefin_accounts WHERE provider_item_id=? AND account_ref=?`, itemID, ref).Scan(&currency, &oldType, &oldSign, &oldIgnore)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("account not discovered; run simplefin-accounts first")
	}
	if err != nil {
		return err
	}
	if !ignore && currency != "USD" {
		return errors.New("only USD accounts can be imported; use --ignore")
	}
	var imported bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE provider_item_id=? AND provider_account_id=?)`, itemID, ref).Scan(&imported); err != nil {
		return err
	}
	if imported && (kind != oldType || sign != oldSign || ignore != oldIgnore) {
		return errors.New("account already imported; changing type/sign/ignore requires a reviewed data migration")
	}
	var typeValue, signValue any
	if !ignore {
		typeValue = kind
		signValue = sign
	}
	_, err = tx.ExecContext(ctx, `UPDATE simplefin_accounts SET type=?,balance_sign=?,ignored=? WHERE provider_item_id=? AND account_ref=?`, typeValue, signValue, ignore, itemID, ref)
	if err != nil {
		return err
	}
	return tx.Commit()
}
