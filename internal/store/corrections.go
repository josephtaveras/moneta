package store

import (
	"context"
	"database/sql"
	"errors"
)

type CategoryRow struct {
	ID   int64
	Name string
	Kind string
}

func ListCategories(ctx context.Context, db *sql.DB) ([]CategoryRow, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	rows, err := db.QueryContext(ctx, `SELECT id,name,kind FROM categories ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []CategoryRow{}
	for rows.Next() {
		var row CategoryRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type TransactionCorrection struct {
	CategoryID *int64 // zero explicitly means uncategorized
	Transfer   *bool
	Excluded   *bool
}

// CorrectTransaction locks the effective classification against provider
// re-import. Transfers are always excluded; clearing the transfer flag leaves
// exclusion unchanged unless Excluded is also supplied. No counterparty is
// guessed.
func CorrectTransaction(ctx context.Context, db *sql.DB, id int64, change TransactionCorrection) error {
	if db == nil {
		return errors.New("database is required")
	}
	if id <= 0 || (change.CategoryID == nil && change.Transfer == nil && change.Excluded == nil) {
		return errors.New("transaction id and a correction are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var category sql.NullInt64
	var transfer, excluded bool
	err = tx.QueryRowContext(ctx, `SELECT category_id,is_transfer,excluded FROM transactions WHERE id=?`, id).Scan(&category, &transfer, &excluded)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("transaction not found")
	}
	if err != nil {
		return err
	}
	if change.CategoryID != nil {
		category = sql.NullInt64{Int64: *change.CategoryID, Valid: *change.CategoryID != 0}
		if category.Valid {
			var kind string
			if err = tx.QueryRowContext(ctx, `SELECT kind FROM categories WHERE id=?`, category.Int64).Scan(&kind); err != nil {
				return errors.New("category not found")
			}
			transfer = kind == "transfer"
			excluded = transfer || kind == "ignore"
		} else {
			transfer = false
			excluded = false
		}
	}
	if change.Transfer != nil {
		transfer = *change.Transfer
		if transfer {
			excluded = true
		}
	}
	if change.Excluded != nil {
		excluded = *change.Excluded
	}
	if transfer && !excluded {
		return errors.New("a transfer must be excluded from spending")
	}
	_, err = tx.ExecContext(ctx, `UPDATE transactions SET category_id=?,is_transfer=?,excluded=?,classification_manual=1,
 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, category, transfer, excluded, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
