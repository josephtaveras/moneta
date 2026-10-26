package main

import (
	"bytes"
	"context"
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
	_ "time/tzdata"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

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
	call := func(want int, args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		code := run(ctx, args, &out, &stderr)
		if strings.Contains(out.String()+stderr.String(), "fake-secret") || strings.Contains(out.String()+stderr.String(), access) {
			t.Fatal("credential leaked")
		}
		if code != want {
			t.Fatalf("%v exit %d, want %d: %s %s", args, code, want, out.String(), stderr.String())
		}
		return out.String() + stderr.String()
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
	if want := now.In(location).Format(time.DateOnly); !strings.Contains(call(0, "simplefin-accounts", "--json"), want) || want == now.Format(time.DateOnly) {
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
		if err := json.Unmarshal([]byte(call(0, "spend", "--period", now.Format("2006-01"), "--json")), &doc); err != nil {
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
	if out := call(0, "networth", "--json"); !strings.Contains(out, `"networth":950`) {
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
	if out := call(0, "sync"); !strings.Contains(out, "warning: SimpleFIN reported 1 connection warning(s)") {
		t.Fatalf("missing connection warning: %s", out)
	}
	legacyErrors = "[]"
	// A fractional-cent amount skips only that row; the earlier value stays.
	amount = "-120.005"
	if out := call(0, "sync"); !strings.Contains(out, " 1 skipped.") {
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
	if out := call(0, "sync"); !strings.Contains(out, "in 3 passes") || requests != 5 {
		t.Fatalf("catch-up requests=%d: %s", requests, out)
	}
	assertCursorNear(time.Now())
	// Beyond the per-invocation bound, progress is kept and the user is told to rerun.
	start := time.Now().Add(-1000 * 24 * time.Hour)
	setCursor(start)
	requests = 0
	if out := call(1, "sync"); !strings.Contains(out, "run moneta sync again") || requests != 20 {
		t.Fatalf("bounded catch-up requests=%d: %s", requests, out)
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
