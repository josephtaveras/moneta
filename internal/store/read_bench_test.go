package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// benchTransactionCount is the seeded corpus size for the read benchmarks:
// several years of dense activity, comfortably past any realistic personal
// or small-business history.
const benchTransactionCount = 10_000

// seedBenchTransactions opens a fresh database in a temp directory and fills
// it with count synthetic transactions spread over two accounts. Values are
// neutral and generated; no real financial data enters this repository.
func seedBenchTransactions(b *testing.B, count int) *sql.DB {
	b.Helper()

	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "moneta.db"))
	if err != nil {
		b.Fatalf("Open() error: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })

	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		b.Fatalf("EnsureDefaultEntity() error: %v", err)
	}

	accountIDs := make([]int64, 0, 2)
	for i, account := range []struct{ name, kind string }{
		{"Everyday Checking", "checking"},
		{"Travel Card", "credit_card"},
	} {
		result, err := db.ExecContext(ctx, `
			INSERT INTO accounts (
				entity_id, type, name, institution, provider, provider_account_id
			) VALUES (?, ?, ?, 'Test Institution', 'plaid', ?)
		`, entityID, account.kind, account.name, fmt.Sprintf("bench-acct-%d", i))
		if err != nil {
			b.Fatalf("insert account %q: %v", account.name, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			b.Fatalf("read account id: %v", err)
		}
		accountIDs = append(accountIDs, id)
	}

	merchants := []string{
		"Grocery Mart", "Coffee Place", "Stream Co", "Transit Authority",
		"Corner Pharmacy", "Fuel Stop", "Hardware Depot", "Employer Inc",
	}
	start := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

	// One transaction for the whole seed: per-row autocommit would dominate
	// setup time and tell us nothing about read performance.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin seed transaction: %v", err)
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents,
			merchant_raw, merchant_norm, status, excluded, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		b.Fatalf("prepare seed insert: %v", err)
	}
	for i := range count {
		merchant := merchants[i%len(merchants)]
		excluded := 0
		if i%17 == 0 {
			excluded = 1 // transfers and card payments leave the aggregates
		}
		if _, err := statement.ExecContext(ctx,
			accountIDs[i%len(accountIDs)],
			entityID,
			start.AddDate(0, 0, -(i/8)).Format("2006-01-02"),
			int64(-125-(i*997)%48_500),
			merchant,
			merchant,
			"posted",
			excluded,
			fmt.Sprintf("bench-hash-%d", i),
		); err != nil {
			b.Fatalf("insert seed transaction %d: %v", i, err)
		}
	}
	if err := statement.Close(); err != nil {
		b.Fatalf("close seed statement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed transactions: %v", err)
	}
	return db
}

// BenchmarkListTransactions measures the row read behind `moneta tx` and
// GET /v1/transactions over a 10k-transaction database: the default 20-row
// window, a 500-row page, and the unbounded --full scan.
func BenchmarkListTransactions(b *testing.B) {
	ctx := context.Background()
	db := seedBenchTransactions(b, benchTransactionCount)

	for _, limit := range []int{20, 500, 0} {
		name := fmt.Sprintf("limit=%d", limit)
		if limit == 0 {
			name = "full"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, err := ListTransactions(ctx, db, TransactionFilter{}, limit)
				if err != nil {
					b.Fatalf("ListTransactions() error: %v", err)
				}
				if len(rows) == 0 {
					b.Fatal("ListTransactions() returned no rows")
				}
			}
		})
	}
}

// BenchmarkSummarizeTransactions measures the pre-computed aggregate that
// every tx read carries: one round trip over all 10k matching rows,
// unfiltered and date-filtered.
func BenchmarkSummarizeTransactions(b *testing.B) {
	ctx := context.Background()
	db := seedBenchTransactions(b, benchTransactionCount)

	for _, filter := range []struct {
		name  string
		value TransactionFilter
	}{
		{"unfiltered", TransactionFilter{}},
		{"month", TransactionFilter{From: "2026-07-01", To: "2026-07-31"}},
		{"search", TransactionFilter{Search: "coffee"}},
	} {
		b.Run(filter.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := SummarizeTransactions(ctx, db, filter.value); err != nil {
					b.Fatalf("SummarizeTransactions() error: %v", err)
				}
			}
		})
	}
}
