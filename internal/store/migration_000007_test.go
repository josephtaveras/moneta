package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration000007ImportRunWarningsAndDetectorReset(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open pre-000007 database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close pre-000007 database: %v", err)
		}
	})
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		) STRICT
	`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for version, name := range []string{
		"000001_initial_schema.up.sql",
		"000002_import_runs_skipped.up.sql",
		"000003_normalize_liability_balance_sign.up.sql",
		"000004_merchant_display_and_card_due_date.up.sql",
		"000005_detector_schema.up.sql",
		"000006_simplefin_and_corrections.up.sql",
	} {
		migrationSQL, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %q: %v", name, err)
		}
		if _, err := db.Exec(string(migrationSQL)); err != nil {
			t.Fatalf("apply migration %q: %v", name, err)
		}
		if _, err := db.Exec(
			"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
			version+1,
			name,
		); err != nil {
			t.Fatalf("record migration %q: %v", name, err)
		}
	}

	// A database that ran Plaid-era detection keeps a partial detector state
	// and detected rows that nothing on the SimpleFIN path refreshes.
	entityID := insertEntity(t, db, "personal", "Personal")
	setBillsDetector(t, db, DetectorState{
		Status:              "partial",
		LastRunAt:           "2026-01-20T00:00:00.000Z",
		LastSuccessAt:       "2026-01-10T00:00:00.000Z",
		LastError:           "provider item unavailable",
		LastSeriesCount:     3,
		LastSkippedOverflow: 1,
	})
	insertBillsRecurring(t, db, entityID, "Plaid-era Gym", "bill", "monthly", -5000, "2026-01-15", true, 15)
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date, source
		) VALUES (?, 'Manual Rent', 'bill', 'monthly', -150000, '2026-11-01', 'manual')
	`, entityID); err != nil {
		t.Fatalf("insert manual recurring item: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO import_runs (provider, status, cursor_after, completed_at)
		VALUES ('simplefin', 'succeeded', '2026-01-20T00:00:00Z', '2026-01-20T00:00:00.000Z')
	`); err != nil {
		t.Fatalf("insert pre-000007 import run: %v", err)
	}
	before, err := ReadBills(ctx, db, "2026-10-27", 30)
	if err != nil {
		t.Fatalf("ReadBills() before 000007: %v", err)
	}
	if _, found := billsByName(before.Items)["Plaid-era Gym"]; !found {
		t.Fatalf("stale partial state did not surface the detected bill before 000007: %+v", before.Items)
	}

	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply migration 000007: %v", err)
	}

	var declaredType string
	var notNull int
	var defaultValue sql.NullString
	if err := db.QueryRow(`
		SELECT type, "notnull", dflt_value
		FROM pragma_table_info('import_runs')
		WHERE name = 'connection_warnings'
	`).Scan(&declaredType, &notNull, &defaultValue); err != nil {
		t.Fatalf("read import_runs.connection_warnings info: %v", err)
	}
	if declaredType != "INTEGER" || notNull != 1 || defaultValue.String != "0" {
		t.Errorf("import_runs.connection_warnings = type %q / notnull %d / default %#v, want INTEGER/1/0",
			declaredType, notNull, defaultValue)
	}
	var warnings int
	if err := db.QueryRow("SELECT connection_warnings FROM import_runs").Scan(&warnings); err != nil {
		t.Fatalf("read existing import run warnings: %v", err)
	}
	if warnings != 0 {
		t.Errorf("existing import run connection_warnings = %d, want 0", warnings)
	}
	for name, statement := range map[string]string{
		"negative":   "UPDATE import_runs SET connection_warnings = -1",
		"fractional": "UPDATE import_runs SET connection_warnings = 1.5",
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("%s connection_warnings was accepted", name)
		}
	}

	state, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("read detector state: %v", err)
	}
	if state != (DetectorState{Status: "never_run"}) {
		t.Errorf("detector state after 000007 = %+v, want the seeded never_run state", state)
	}
	bills, err := ReadBills(ctx, db, "2026-10-27", 30)
	if err != nil {
		t.Fatalf("ReadBills() after 000007: %v", err)
	}
	if _, found := billsByName(bills.Items)["Plaid-era Gym"]; found {
		t.Errorf("bills still surface the stale detected row: %+v", bills.Items)
	}
	dashboard, err := ReadDashboard(ctx, db, DashboardFilter{From: "2026-10-01", To: "2026-10-31", BillsAsOf: "2026-10-27"})
	if err != nil {
		t.Fatalf("ReadDashboard() after 000007: %v", err)
	}
	if dashboard.UpcomingBills != nil {
		t.Errorf("dashboard upcoming bills = %+v, want nil under never_run", dashboard.UpcomingBills)
	}
	recurring, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-10-27"})
	if err != nil {
		t.Fatalf("ReadRecurring() after 000007: %v", err)
	}
	if len(recurring.Items) != 1 || recurring.Items[0].Source != "manual" {
		t.Errorf("recurring items = %+v, want only the manual row", recurring.Items)
	}
	var detectedRows int
	if err := db.QueryRow("SELECT count(*) FROM recurring_items WHERE source = 'detected'").Scan(&detectedRows); err != nil {
		t.Fatalf("count detected rows: %v", err)
	}
	if detectedRows != 1 {
		t.Errorf("detected rows = %d, want 1 kept in storage", detectedRows)
	}

	downSQL, err := migrationFiles.ReadFile("migrations/000007_import_run_warnings_and_detector_reset.down.sql")
	if err != nil {
		t.Fatalf("read migration 000007 down: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply migration 000007 down: %v", err)
	}
	var exists bool
	if err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM pragma_table_info('import_runs') WHERE name = 'connection_warnings'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("check connection_warnings removal: %v", err)
	}
	if exists {
		t.Error("import_runs.connection_warnings still exists after down migration")
	}
	var runs int
	if err := db.QueryRow("SELECT count(*) FROM import_runs").Scan(&runs); err != nil || runs != 1 {
		t.Errorf("import runs after down migration = %d, %v; want 1", runs, err)
	}
	// The reset is deliberately not reversed.
	state, err = ReadDetectorState(ctx, db)
	if err != nil || state.Status != "never_run" {
		t.Errorf("detector state after down migration = %+v, %v; want never_run", state, err)
	}
}
