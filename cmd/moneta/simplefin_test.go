package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

// first returns a command's stdout from its stdout and stderr pair.
func first(stdout, _ string) string { return stdout }

// This walks the user-facing commands against fake HTTPS, including token
// claim, account confirmation, re-import, correction, reports, and failure.
func TestSimpleFINCLIWorkflow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "finance.db")
	t.Setenv(databasePathEnvironment, path)
	t.Setenv("MONETA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	now := time.Now().UTC()
	epoch := now.Unix()
	amount := "-100.00"
	errorBody := ""
	legacyErrors := "[]"
	httpStatus := 200
	requests := 0
	var access string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claim" {
			_, _ = fmt.Fprint(w, access)
			return
		}
		requests++
		if u, p, ok := r.BasicAuth(); !ok || u != "demo" || p != "fake-secret" {
			t.Error("incorrect basic auth")
		}
		if httpStatus != 200 {
			w.WriteHeader(httpStatus)
			_, _ = fmt.Fprint(w, "fake-secret")
			return
		}
		if errorBody != "" {
			_, _ = fmt.Fprint(w, errorBody)
			return
		}
		_, _ = fmt.Fprintf(w, `{"errlist":[],"errors":%s,"connections":[{"conn_id":"bank","name":"Demo Bank"}],"accounts":[
   {"conn_id":"bank","id":"checking","name":"Demo Checking","currency":"USD","balance":"1000.00","balance-date":%d,"transactions":[{"id":"one","posted":%d,"amount":"-100.00","description":"Demo Payment"}]},
   {"conn_id":"bank","id":"card","name":"Demo Card","currency":"USD","balance":"-50.00","balance-date":%d,"transactions":[{"id":"one","posted":%d,"amount":%q,"description":"Demo Purchase"},{"id":"two","posted":%d,"amount":"100.00","description":"Demo Payment Received"}]}]}`, legacyErrors, epoch, epoch-1, epoch, epoch-1, amount, epoch-1)
	}))
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	access = strings.Replace(server.URL, "https://", "https://demo:fake-secret@", 1)
	t.Setenv("SIMPLEFIN_SETUP_TOKEN", base64.StdEncoding.EncodeToString([]byte(server.URL+"/claim")))
	call := func(want int, args ...string) (string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := run(ctx, args, &out, &stderr)
		if strings.Contains(out.String()+stderr.String(), "fake-secret") || strings.Contains(out.String()+stderr.String(), access) {
			t.Fatal("credential leaked")
		}
		if code != want {
			t.Fatalf("%v exit %d, want %d: %s %s", args, code, want, out.String(), stderr.String())
		}
		return out.String(), stderr.String()
	}
	// A blank connection name is a usage error everywhere, before any claim.
	call(2, "link", "--item", "")
	call(2, "sync", "--item", "")
	call(2, "simplefin-accounts", "--item", " ")
	call(2, "configure-account", "--item", " ", "--account", "x", "--type", "checking", "--balance-sign", "1")
	if requests != 0 {
		t.Fatal("usage error reached Bridge")
	}
	call(0, "link")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var ciphertext []byte
	if err := db.QueryRow(`SELECT access_token_enc FROM provider_items`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("fake-secret")) {
		t.Fatal("plaintext credential stored")
	}
	call(1, "sync") // unknown accounts must not guess types or ingest anything
	var cursor, health string
	if err := db.QueryRow(`SELECT sync_cursor,status FROM provider_items`).Scan(&cursor, &health); err != nil || cursor != "" {
		t.Fatal("failed sync advanced checkpoint")
	}
	if health != "ok" {
		t.Fatalf("local configuration failure changed connection health to %q", health)
	}
	t.Setenv("MONETA_TIMEZONE", "Not/A_Zone")
	call(2, "sync")
	call(2, "simplefin-accounts")
	// A valid zone reaches the provider: pick one whose date differs from UTC now.
	zone := "Etc/GMT+12"
	if now.Hour() >= 12 {
		zone = "Pacific/Kiritimati"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONETA_TIMEZONE", zone)
	if want, out := now.In(location).Format(time.DateOnly), first(call(0, "simplefin-accounts", "--json")); !strings.Contains(out, want) || want == now.Format(time.DateOnly) {
		t.Fatalf("MONETA_TIMEZONE=%s did not select balance date %s", zone, want)
	}
	t.Setenv("MONETA_TIMEZONE", "")
	call(0, "simplefin-accounts", "--json")
	accounts, err := store.ListSimpleFINAccounts(ctx, db, 1)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("discovery: %v %v", accounts, err)
	}
	for _, a := range accounts {
		kind, sign := "checking", "1"
		if a.Name == "Demo Card" {
			kind, sign = "credit_card", "-1"
		}
		call(0, "configure-account", "--account", a.ID, "--type", kind, "--balance-sign", sign)
	}
	call(0, "sync")
	assertSpend := func(want float64) {
		t.Helper()
		var doc struct {
			Summary struct {
				Total float64 `json:"total_spend"`
			}
		}
		if err := json.Unmarshal([]byte(first(call(0, "spend", "--period", now.Format("2006-01"), "--json"))), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Summary.Total != want {
			t.Fatalf("spend=%v want %v", doc.Summary.Total, want)
		}
	}
	assertSpend(200) // reproduces the review finding before corrections
	rows, err := store.ListTransactions(ctx, db, store.TransactionFilter{}, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("identity collision: %v %v", rows, err)
	}
	for _, row := range rows {
		if strings.Contains(row.Merchant, "payment") {
			call(0, "correct-tx", "--id", strconv.FormatInt(row.ID, 10), "--transfer", "true")
		}
	}
	var foodID int64
	if err := db.QueryRow(`SELECT id FROM categories WHERE name='Food and Drink'`).Scan(&foodID); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Merchant, "purchase") {
			call(0, "correct-tx", "--id", strconv.FormatInt(row.ID, 10), "--category", strconv.FormatInt(foodID, 10))
		}
	}
	assertSpend(100)
	if out, _ := call(0, "networth", "--json"); !strings.Contains(out, `"networth":950`) {
		t.Fatalf("net worth wrong: %s", out)
	}
	call(0, "categories", "--json")
	call(0, "tx", "--review", "--full", "--json")
	amount = "-120.00"
	call(0, "sync")
	assertSpend(120)
	var count, manual, excluded int
	if err := db.QueryRow(`SELECT count(*),sum(classification_manual),sum(excluded) FROM transactions`).Scan(&count, &manual, &excluded); err != nil {
		t.Fatal(err)
	}
	if count != 3 || manual != 3 || excluded != 2 {
		t.Fatalf("reimport corrupted corrections: %d %d %d", count, manual, excluded)
	}
	var categoryCount int
	if err := db.QueryRow(`SELECT count(*) FROM transactions WHERE category_id=?`, foodID).Scan(&categoryCount); err != nil || categoryCount != 1 {
		t.Fatal("category override lost")
	}
	if err := db.QueryRow(`SELECT sync_cursor FROM provider_items`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	errorBody = `{"accounts":[],"errlist":[{"code":"act.missingdata"}]}`
	call(1, "sync")
	var after string
	if err := db.QueryRow(`SELECT sync_cursor,status FROM provider_items`).Scan(&after, &health); err != nil || cursor != after || health != "error" {
		t.Fatalf("partial health/checkpoint: %s %s %s %v", cursor, after, health, err)
	}
	errorBody = ""
	httpStatus = 403
	call(1, "sync")
	call(3, "status", "--json")
	httpStatus = 200
	// A successful discovery request proves the connection is healthy again.
	call(0, "simplefin-accounts")
	call(0, "status", "--json")
	call(0, "sync")
	call(0, "status", "--json")
	// Informational Bridge messages warn without failing or leaking their text.
	legacyErrors = `["fake-secret institution notice"]`
	if out, errOut := call(0, "sync"); !strings.Contains(out, "SimpleFIN pass 1: 2 accounts, 3 posted records received, 0 skipped, 1 connection warnings. Checkpoint ") ||
		!strings.Contains(errOut, "warning: SimpleFIN pass 1 reported 1 connection warning(s)") || strings.Contains(out, "warning:") {
		t.Fatalf("missing connection warning:\nstdout: %s\nstderr: %s", out, errOut)
	}
	legacyErrors = "[]"
	// A fractional-cent amount skips only that row; the earlier value stays.
	amount = "-120.005"
	if out, _ := call(0, "sync"); !strings.Contains(out, " 1 skipped, ") {
		t.Fatalf("fractional cents not skipped: %s", out)
	}
	assertSpend(120)
	amount = "-120.00"
	// An old checkpoint catches up fully in one invocation.
	setCursor := func(at time.Time) {
		t.Helper()
		if _, err := db.Exec(`UPDATE provider_items SET sync_cursor=?`, at.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	assertCursorNear := func(want time.Time) {
		t.Helper()
		if err := db.QueryRow(`SELECT sync_cursor FROM provider_items`).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		got, err := time.Parse(time.RFC3339, cursor)
		if err != nil || got.Sub(want).Abs() > time.Minute {
			t.Fatalf("checkpoint %s, want near %s", cursor, want.Format(time.RFC3339))
		}
	}
	setCursor(time.Now().Add(-200 * 24 * time.Hour))
	requests = 0
	if out, errOut := call(0, "sync"); strings.Count(out, "SimpleFIN pass ") != 3 || !strings.Contains(out, "SimpleFIN pass 3: ") ||
		!strings.Contains(out, "Caught up from an older checkpoint in 3 passes.") || errOut != "" || requests != 5 {
		t.Fatalf("catch-up requests=%d:\nstdout: %s\nstderr: %s", requests, out, errOut)
	}
	assertCursorNear(time.Now())
	// Beyond the per-invocation bound, progress is kept and the user is told
	// to rerun after Bridge's daily request budget resets, not right away.
	start := time.Now().Add(-1000 * 24 * time.Hour)
	setCursor(start)
	requests = 0
	if out, errOut := call(1, "sync"); strings.Count(out, "SimpleFIN pass ") != simplefinMaxPasses || strings.Contains(out, "Caught up") ||
		!strings.Contains(errOut, "still behind after 10 passes") || !strings.Contains(errOut, "run moneta sync again tomorrow") ||
		strings.Contains(errOut, "again to continue") || requests != 20 {
		t.Fatalf("bounded catch-up requests=%d:\nstdout: %s\nstderr: %s", requests, out, errOut)
	}
	assertCursorNear(start.Add(simplefinMaxPasses * 85 * 24 * time.Hour))
	call(0, "sync")
	assertCursorNear(time.Now())
	// SimpleFIN sync and corrections never run recurring detection.
	if err := db.QueryRow(`SELECT status FROM detector_state`).Scan(&health); err != nil || health != "never_run" {
		t.Fatalf("detector state = %q; advanced detection should stay paused", health)
	}
	// Account signs cannot be silently reinterpreted after history exists.
	call(1, "configure-account", "--account", accounts[0].ID, "--type", "asset", "--balance-sign", "1")
}

// bridgeFixture is a fake Bridge serving one checking account. Like the real
// service, it returns only the posted rows inside each request's start-date
// and end-date window, so tests can observe which history a sync reaches.
type bridgeFixture struct {
	t  *testing.T
	db *sql.DB
	// warnings is the JSON legacy errors array returned with every response.
	warnings string
	// posted maps a transaction ID to its posted time.
	posted map[string]time.Time
	// starts records the start-date of each windowed request in order.
	starts []time.Time
	// failFrom makes this request number and later ones fail with HTTP 500;
	// zero never fails.
	failFrom int
	requests int
}

const bridgeNotice = "bridge-notice-text"

func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "finance.db")
	t.Setenv(databasePathEnvironment, path)
	t.Setenv("MONETA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	t.Setenv("MONETA_TIMEZONE", "")
	f := &bridgeFixture{t: t, warnings: "[]", posted: map[string]time.Time{}}
	var access string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claim" {
			_, _ = fmt.Fprint(w, access)
			return
		}
		f.requests++
		if f.failFrom > 0 && f.requests >= f.failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		query := r.URL.Query()
		rows := []string{}
		if query.Has("start-date") {
			start, _ := strconv.ParseInt(query.Get("start-date"), 10, 64)
			end, _ := strconv.ParseInt(query.Get("end-date"), 10, 64)
			f.starts = append(f.starts, time.Unix(start, 0).UTC())
			for id, at := range f.posted {
				if at.Unix() >= start && at.Unix() < end {
					rows = append(rows, fmt.Sprintf(`{"id":%q,"posted":%d,"amount":"-42.00","description":"Demo %s"}`, id, at.Unix(), id))
				}
			}
		}
		_, _ = fmt.Fprintf(w, `{"errlist":[],"errors":%s,"connections":[{"conn_id":"bank","name":"Demo Bank"}],"accounts":[{"conn_id":"bank","id":"checking","name":"Demo Checking","currency":"USD","balance":"1000.00","balance-date":%d,"transactions":[%s]}]}`, f.warnings, time.Now().Unix(), strings.Join(rows, ","))
	}))
	t.Cleanup(server.Close)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	access = strings.Replace(server.URL, "https://", "https://demo:fake-secret@", 1)
	t.Setenv("SIMPLEFIN_SETUP_TOKEN", base64.StdEncoding.EncodeToString([]byte(server.URL+"/claim")))
	f.run(0, "link")
	f.run(0, "simplefin-accounts")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.db = db
	accounts, err := store.ListSimpleFINAccounts(ctx, db, 1)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("discovery: %v %v", accounts, err)
	}
	f.run(0, "configure-account", "--account", accounts[0].ID, "--type", "checking", "--balance-sign", "1")
	return f
}

// run executes one command, requires its exit code, and returns stdout and
// stderr separately. Credentials and Bridge message text must never appear.
func (f *bridgeFixture) run(want int, args ...string) (string, string) {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	if combined := stdout.String() + stderr.String(); strings.Contains(combined, "fake-secret") || strings.Contains(combined, bridgeNotice) {
		f.t.Fatal("credential or Bridge message text leaked")
	}
	if code != want {
		f.t.Fatalf("%v exit %d, want %d:\nstdout: %s\nstderr: %s", args, code, want, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func (f *bridgeFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bridgeFixture) checkpoint() time.Time {
	f.t.Helper()
	var cursor string
	if err := f.db.QueryRow(`SELECT sync_cursor FROM provider_items`).Scan(&cursor); err != nil {
		f.t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		f.t.Fatalf("checkpoint %q: %v", cursor, err)
	}
	return at
}

func (f *bridgeFixture) imported(id string) bool {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM transactions WHERE merchant_raw = ?`, "Demo "+id).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count == 1
}

const day = 24 * time.Hour

// A sync that carries connection warnings still advances the checkpoint.
// Rows a recovered institution backfills behind that checkpoint must still
// import on a later warning-free sync, without --reset-cursor.
func TestSimpleFINSyncReachesBackPastConnectionWarnings(t *testing.T) {
	f := newBridgeFixture(t)
	f.run(0, "sync")
	// The last warning-free sync committed twelve days ago.
	lastClean := time.Now().UTC().Add(-12 * day).Truncate(time.Second)
	f.exec(`UPDATE import_runs SET cursor_after = ?`, lastClean.Format(time.RFC3339))
	f.exec(`UPDATE provider_items SET sync_cursor = ?`, lastClean.Format(time.RFC3339))
	// The institution is stale: Bridge warns and returns no new rows.
	f.warnings = fmt.Sprintf("[%q]", bridgeNotice)
	stdout, stderr := f.run(0, "sync")
	warned := f.checkpoint()
	if time.Since(warned) > time.Minute {
		t.Fatalf("warning sync did not advance the checkpoint: %s", warned)
	}
	if want := fmt.Sprintf("SimpleFIN pass 1: 1 accounts, 0 posted records received, 0 skipped, 1 connection warnings. Checkpoint %s.\n", warned.Format(time.RFC3339)); !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want pass line %q", stdout, want)
	}
	if !strings.Contains(stderr, "warning: SimpleFIN pass 1 reported 1 connection warning(s)") || !strings.Contains(stderr, "last warning-free sync") {
		t.Errorf("stderr = %q, want a pass warning that explains the automatic lookback", stderr)
	}
	// The institution recovers, and Bridge backfills a row posted ten days
	// before the warning sync, outside the usual five-day overlap.
	f.warnings = "[]"
	f.posted["late"] = warned.Add(-10 * day)
	f.starts = nil
	stdout, stderr = f.run(0, "sync")
	if !f.imported("late") {
		t.Fatalf("backfilled row was not imported:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if len(f.starts) == 0 || !f.starts[0].Equal(lastClean.Add(-5*day)) {
		t.Errorf("recovery request starts = %v, want the overlap before %s", f.starts, lastClean)
	}
	if !strings.Contains(stdout, "Rechecking from "+lastClean.Format(time.RFC3339)) || stderr != "" {
		t.Errorf("recovery output:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// After a warning-free sync commits, the overlap is the usual five days.
	recovered := f.checkpoint()
	f.starts = nil
	stdout, _ = f.run(0, "sync")
	if len(f.starts) != 1 || !f.starts[0].Equal(recovered.Add(-5*day)) || strings.Contains(stdout, "Rechecking") {
		t.Errorf("after recovery request starts = %v, want one request from %s:\n%s", f.starts, recovered.Add(-5*day), stdout)
	}
	rows, err := f.db.Query(`SELECT connection_warnings FROM import_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var recorded []int
	for rows.Next() {
		var warnings int
		if err := rows.Scan(&warnings); err != nil {
			t.Fatal(err)
		}
		recorded = append(recorded, warnings)
	}
	if fmt.Sprint(recorded) != "[0 1 0 0]" {
		t.Errorf("import run connection warnings = %v, want [0 1 0 0]", recorded)
	}
}

// An institution that keeps warning must not grow the lookback without
// bound: it reaches at most 90 days before the stored checkpoint.
func TestSimpleFINSyncWarningLookbackStopsAtNinetyDays(t *testing.T) {
	f := newBridgeFixture(t)
	f.run(0, "sync")
	stored := f.checkpoint()
	// The only warning-free sync is 200 days old; every later sync warned.
	f.exec(`UPDATE import_runs SET cursor_after = ?`, stored.Add(-200*day).Format(time.RFC3339))
	f.warnings = fmt.Sprintf("[%q]", bridgeNotice)
	floor := stored.Add(-90 * day)
	f.posted["inside"] = floor.Add(-2 * day)
	f.posted["outside"] = floor.Add(-10 * day)
	f.starts = nil
	stdout, stderr := f.run(0, "sync")
	if len(f.starts) == 0 || !f.starts[0].Equal(floor.Add(-5*day)) {
		t.Fatalf("lookback request starts = %v, want the overlap before %s", f.starts, floor)
	}
	if !f.imported("inside") || f.imported("outside") {
		t.Errorf("imported inside=%v outside=%v, want only the row inside the 90-day lookback", f.imported("inside"), f.imported("outside"))
	}
	// The lookback is longer than one 85-day pass, so it continues to now.
	if time.Since(f.checkpoint()) > time.Minute || !strings.Contains(stdout, "Rechecking from "+floor.Format(time.RFC3339)) ||
		!strings.Contains(stdout, "Caught up from an older checkpoint in 2 passes.") || strings.Count(stderr, "warning: SimpleFIN pass ") != 2 {
		t.Errorf("lookback output:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// While warnings persist, every sync uses the same bounded lookback.
	next := f.checkpoint()
	f.starts = nil
	f.run(0, "sync")
	if len(f.starts) == 0 || !f.starts[0].Equal(next.Add(-95*day)) {
		t.Errorf("repeat lookback request starts = %v, want the overlap before %s", f.starts, next.Add(-90*day))
	}
}

// Each pass reports its own counts as it commits, so a warning from an
// earlier pass survives a later pass failure.
func TestSimpleFINSyncReportsEachPassBeforeALaterFailure(t *testing.T) {
	f := newBridgeFixture(t)
	f.run(0, "sync")
	start := time.Now().UTC().Add(-200 * day).Truncate(time.Second)
	f.exec(`UPDATE provider_items SET sync_cursor = ?`, start.Format(time.RFC3339))
	f.warnings = fmt.Sprintf("[%q]", bridgeNotice)
	f.failFrom = f.requests + 3 // the first request of the second pass
	stdout, stderr := f.run(1, "sync")
	committed := start.Add(85 * day).Format(time.RFC3339)
	if want := "SimpleFIN pass 1: 1 accounts, 0 posted records received, 0 skipped, 1 connection warnings. Checkpoint " + committed + ".\n"; stdout != want {
		t.Errorf("stdout = %q, want only %q", stdout, want)
	}
	for _, want := range []string{
		"warning: SimpleFIN pass 1 reported 1 connection warning(s)",
		"note: 1 earlier sync pass(es) committed through checkpoint " + committed,
		"error: sync provider item: SimpleFIN HTTP status 500",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if got := f.checkpoint().Format(time.RFC3339); got != committed {
		t.Errorf("checkpoint = %s, want the first pass %s", got, committed)
	}
}

func TestSimpleFINPullCursor(t *testing.T) {
	stored := "2026-10-01T00:00:00Z"
	for _, tc := range []struct {
		name, stored, clean, want string
	}{
		{"first sync", "", "", ""},
		{"invalid checkpoint is left for the provider to reject", "not a checkpoint", "", "not a checkpoint"},
		{"clean checkpoint is the stored checkpoint", stored, stored, stored},
		{"clean checkpoint is later", stored, "2026-10-02T00:00:00Z", stored},
		{"clean checkpoint is earlier", stored, "2026-09-19T00:00:00Z", "2026-09-19T00:00:00Z"},
		{"clean checkpoint is older than the limit", stored, "2026-03-01T00:00:00Z", "2026-07-03T00:00:00Z"},
		{"no clean checkpoint", stored, "", "2026-07-03T00:00:00Z"},
	} {
		if got := simplefinPullCursor(tc.stored, tc.clean); got != tc.want {
			t.Errorf("%s: simplefinPullCursor(%q, %q) = %q, want %q", tc.name, tc.stored, tc.clean, got, tc.want)
		}
	}
}
