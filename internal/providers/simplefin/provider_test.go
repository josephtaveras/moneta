package simplefin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

func TestMoneyExactAndSafe(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{{"0", 0}, {"-0.01", -1}, {"12.3400", 1234}, {"92233720368547758.07", 9223372036854775807}, {"-92233720368547758.07", -9223372036854775807}} {
		got, err := Money(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("Money(%q)=%d,%v", tc.in, got, err)
		}
	}
	for _, s := range []string{"", "1.001", "NaN", "1e2", "+1", " 1", ".1", "1.", "1..2", "92233720368547758.08", "-92233720368547758.08"} {
		if _, err := Money(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if _, err := Money("-20.001"); !errors.Is(err, errFractionalCents) {
		t.Errorf("fractional cents error = %v", err)
	}
	if _, err := Money("92233720368547758.08"); errors.Is(err, errFractionalCents) {
		t.Error("overflow reported as fractional cents")
	}
}
func TestClaimAndRequestsNeverExposeSecretsOrFollowRedirects(t *testing.T) {
	secret := "never-print-this"
	var endpoint string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/claim":
			if r.Method != "POST" {
				t.Error("not POST")
			}
			_, _ = fmt.Fprint(w, strings.Replace(endpoint, "https://", "https://user:"+secret+"@", 1))
		case "/redirect":
			w.Header().Set("Location", endpoint+"/leak")
			w.WriteHeader(302)
		case "/leak":
			t.Error("followed redirect")
		default:
			w.WriteHeader(403)
			_, _ = fmt.Fprint(w, secret)
		}
	}))
	defer server.Close()
	endpoint = server.URL
	token := base64.StdEncoding.EncodeToString([]byte(endpoint + "/claim"))
	access, err := claim(context.Background(), token, server.Client())
	if err != nil || !strings.Contains(access, secret) {
		t.Fatalf("claim failed: %v", err)
	}
	for _, path := range []string{"/redirect", "/denied"} {
		_, err := claim(context.Background(), base64.StdEncoding.EncodeToString([]byte(endpoint+path)), server.Client())
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), endpoint) {
			t.Fatalf("unsafe claim error: %v", err)
		}
	}
	if _, err := New("http://user:secret@example.com", "default", nil); err == nil {
		t.Fatal("accepted HTTP")
	}
	u, _ := url.Parse(access)
	server.Close()
	_, err = request(context.Background(), server.Client(), "GET", u, 100)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), endpoint) {
		t.Fatalf("unsafe transport error: %v", err)
	}
}
func TestSyncWindowsScopesSignsAndPostedOnly(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	var query url.Values
	payload := fmt.Sprintf(`{"errlist":[],"accounts":[
 {"conn_id":"bank-a","id":"same","name":"Checking","currency":"USD","balance":"100.01","balance-date":%d,"transactions":[{"id":"same-tx","posted":%d,"amount":"-2.25","description":"Coffee"},{"id":"pending","pending":true,"posted":0,"amount":"1"}]},
 {"conn_id":"bank-b","id":"same","name":"Card","currency":"USD","balance":"-50.00","balance-date":%d,"transactions":[{"id":"same-tx","posted":%d,"amount":"-3.25","description":"Coffee"}]}]}`, now.Unix(), now.Unix()-1, now.Unix(), now.Unix()-1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "secret" {
			t.Error("missing auth")
		}
		query = r.URL.Query()
		_, _ = fmt.Fprint(w, payload)
	}))
	defer server.Close()
	config := map[string]AccountConfig{ScopedID("default", "bank-a", "same"): {Type: canon.AccountTypeChecking, BalanceSign: 1}, ScopedID("default", "bank-b", "same"): {Type: canon.AccountTypeCreditCard, BalanceSign: -1}}
	p, err := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", config)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	p.now = func() time.Time { return now }
	batch, err := p.Sync(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if batch.HasMore || len(batch.Added) != 2 || batch.Added[0].ProviderTxnID == batch.Added[1].ProviderTxnID || batch.Balances[1].CurrentCents != 5000 {
		t.Fatalf("wrong normalization: %#v", batch)
	}
	if query.Get("version") != "2" || query.Has("pending") {
		t.Fatalf("query: %v", query)
	}
	start, _ := strconv.ParseInt(query.Get("start-date"), 10, 64)
	end, _ := strconv.ParseInt(query.Get("end-date"), 10, 64)
	if end-start != 45*86400 {
		t.Fatalf("window=%d", end-start)
	}
	checkpoint := now.Add(-10 * 24 * time.Hour)
	if batch, err := p.Sync(context.Background(), checkpoint.Format(time.RFC3339)); err != nil || batch.HasMore || batch.NextCursor != now.Format(time.RFC3339) {
		t.Fatalf("recent checkpoint: %v", err)
	}
	if query.Get("start-date") != strconv.FormatInt(checkpoint.Add(-5*24*time.Hour).Unix(), 10) {
		t.Fatal("missing overlap")
	}
	old := now.Add(-200 * 24 * time.Hour)
	batch, err = p.Sync(context.Background(), old.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if batch.NextCursor != old.Add(85*24*time.Hour).Format(time.RFC3339) || !batch.HasMore {
		t.Fatalf("unbounded or unflagged catchup: %s %v", batch.NextCursor, batch.HasMore)
	}
	if _, err := p.Sync(context.Background(), "not a checkpoint"); !errors.Is(err, ErrLocalConfiguration) {
		t.Fatalf("invalid checkpoint error = %v", err)
	}
	delete(config, ScopedID("default", "bank-b", "same"))
	if _, err := p.Sync(context.Background(), ""); !errors.Is(err, ErrLocalConfiguration) {
		t.Fatalf("unknown account error = %v", err)
	}
	config[ScopedID("default", "bank-b", "same")] = AccountConfig{Ignore: true}
	if batch, err := p.Sync(context.Background(), ""); err != nil || len(batch.Accounts) != 1 {
		t.Fatalf("ignore: %v", err)
	}
}
func TestRejectsIncompleteOrMalformedAccountSets(t *testing.T) {
	for _, tc := range []struct {
		body string
		auth bool
	}{
		{`{"accounts":[],"errlist":[{"code":"act.missingdata"}]}`, false},
		{`{"accounts":[],"errlist":[{"code":"con.auth"}]}`, true},
		{`{"accounts":[],"errlist":[{"code":"con.unrecognized","msg":"secret response"}],"errors":["secret response"]}`, false},
		{`{}`, false}, {`{"accounts":null}`, false}, {`not JSON secret`, false},
		{`{"accounts":[{"id":"x","name":"x","currency":"USD"}]}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, tc.body) }))
			defer server.Close()
			p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", nil)
			p.client = server.Client()
			_, err := p.Sync(context.Background(), "")
			if err == nil || strings.Contains(err.Error(), "secret response") || errors.Is(err, ErrAuthentication) != tc.auth || errors.Is(err, ErrLocalConfiguration) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// Legacy errors entries are informational per-connection messages. They must
// not abort the sync, and their text must never reach the batch or an error.
func TestConnectionWarningsDoNotAbortSync(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"errors":["secret response A","secret response B","secret response A"],"accounts":[{"conn_id":"bank","id":"a","name":"Checking","currency":"USD","balance":"20.00","balance-date":%d,"transactions":[{"id":"one","posted":%d,"amount":"-1.00","description":"Demo"}]}]}`, now.Unix(), now.Unix()-1)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", map[string]AccountConfig{ScopedID("default", "bank", "a"): {Type: canon.AccountTypeChecking, BalanceSign: 1}})
	p.client = server.Client()
	p.now = func() time.Time { return now }
	batch, err := p.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("warnings aborted sync: %v", err)
	}
	// Both 45-day windows repeat the same messages; only distinct ones count.
	if requests != 2 || batch.ConnectionWarnings != 2 || len(batch.Added) != 1 || batch.NextCursor != now.Format(time.RFC3339) {
		t.Fatalf("requests=%d batch=%#v", requests, batch)
	}
	if strings.Contains(fmt.Sprintf("%#v", batch), "secret response") {
		t.Fatal("warning text leaked into the batch")
	}
	accounts, err := p.Discover(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("warnings aborted discovery: %v", err)
	}
	empty := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"accounts":[],"errors":["secret response"]}`)
	}))
	defer empty.Close()
	p, _ = New(strings.Replace(empty.URL, "https://", "https://user:secret@", 1), "default", nil)
	p.client = empty.Client()
	if batch, err := p.Sync(context.Background(), ""); err != nil || batch.ConnectionWarnings != 1 {
		t.Fatalf("warning-only response: %v", err)
	}
}

// Calendar dates use UTC by default and the configured zone when set, for
// both posted and balance timestamps.
func TestCalendarDatesUseConfiguredLocation(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 22:30 on March 10 in New York is 02:30 UTC on March 11.
	evening := time.Date(2026, 3, 10, 22, 30, 0, 0, newYork)
	now := evening.Add(time.Hour)
	body := fmt.Sprintf(`{"accounts":[{"conn_id":"bank","id":"a","name":"Checking","currency":"USD","balance":"20.00","balance-date":%d,"transactions":[{"id":"one","posted":%d,"amount":"-1.00","description":"Demo"}]}]}`, evening.Unix(), evening.Unix())
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
	defer server.Close()
	for _, tc := range []struct {
		location *time.Location
		want     canon.Date
	}{{nil, "2026-03-11"}, {time.UTC, "2026-03-11"}, {newYork, "2026-03-10"}} {
		p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", map[string]AccountConfig{ScopedID("default", "bank", "a"): {Type: canon.AccountTypeChecking, BalanceSign: 1}})
		p.client = server.Client()
		p.now = func() time.Time { return now }
		if tc.location != nil {
			p.SetLocation(tc.location)
		}
		batch, err := p.Sync(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if batch.Added[0].Date != tc.want || batch.Balances[0].Date != tc.want {
			t.Fatalf("location %v: posted %s balance %s, want %s", tc.location, batch.Added[0].Date, batch.Balances[0].Date, tc.want)
		}
		accounts, err := p.Discover(context.Background())
		if err != nil || accounts[0].BalanceDate != string(tc.want) {
			t.Fatalf("location %v: discovery date %v %v", tc.location, accounts, err)
		}
	}
}

func TestSyncRejectsInvalidMoneyDatesAndDuplicateTransactions(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	valid := fmt.Sprintf(`{"accounts":[{"conn_id":"bank","id":"a","name":"Checking","currency":"USD","balance":"20.00","available-balance":"15.00","balance-date":%d,"transactions":[{"id":"one","posted":%d,"amount":"-1.00","description":"Demo"},{"id":"two","posted":%d,"amount":"-2.00","description":"Demo"}]}]}`, now.Unix(), now.Unix()-1, now.Unix()-1)
	account := ScopedID("default", "bank", "a")
	// A rejected amount skips only its own record; the rest still imports.
	for _, tc := range []struct {
		body         string
		want         canon.SkippedRecord
		balances     int
		transactions int
	}{
		{strings.Replace(valid, `"20.00"`, `"20.001"`, 1), canon.SkippedRecord{Kind: canon.RecordKindBalance, ID: account, Reason: canon.SkipMalformedRecord, Detail: "fractional_cents"}, 0, 2},
		{strings.Replace(valid, `"15.00"`, `"15.009"`, 1), canon.SkippedRecord{Kind: canon.RecordKindBalance, ID: account, Reason: canon.SkipMalformedRecord, Detail: "available_fractional_cents"}, 1, 2},
		{strings.Replace(valid, `"-1.00"`, `"-1.005"`, 1), canon.SkippedRecord{Kind: canon.RecordKindTransaction, ID: ScopedID(account, "one"), Reason: canon.SkipMalformedRecord, Detail: "fractional_cents"}, 1, 1},
		{strings.Replace(valid, `"-1.00"`, `"-92233720368547758.08"`, 1), canon.SkippedRecord{Kind: canon.RecordKindTransaction, ID: ScopedID(account, "one"), Reason: canon.SkipMalformedRecord, Detail: "invalid_amount"}, 1, 1},
	} {
		requests := 0
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			_, _ = fmt.Fprint(w, tc.body)
		}))
		p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", map[string]AccountConfig{account: {Type: canon.AccountTypeChecking, BalanceSign: 1}})
		p.client = server.Client()
		p.now = func() time.Time { return now }
		batch, err := p.Sync(context.Background(), "")
		server.Close()
		if err != nil {
			t.Fatalf("one malformed amount failed the sync: %v", err)
		}
		// Both windows return the same rows; the skip is reported once.
		if requests != 2 || len(batch.Skipped) != 1 || batch.Skipped[0] != tc.want || len(batch.Accounts) != 1 || len(batch.Balances) != tc.balances || len(batch.Added) != tc.transactions {
			t.Fatalf("batch=%#v", batch)
		}
		if strings.Contains(fmt.Sprintf("%#v", batch.Skipped), ".00") {
			t.Fatal("skipped record carried an amount")
		}
		if strings.HasPrefix(tc.want.Detail, "available_") && (batch.Balances[0].CurrentCents != 2000 || batch.Balances[0].AvailableCents != nil) {
			t.Fatalf("available-balance skip lost the current balance: %#v", batch.Balances[0])
		}
	}
	for _, body := range []string{
		strings.Replace(valid, fmt.Sprint(now.Unix()-1), "0", 1),
		strings.Replace(valid, `"USD"`, `"EUR"`, 1),
		strings.Replace(valid, `"id":"one"`, `"id":""`, 1),
		strings.Replace(valid, `"description":"Demo"}`, fmt.Sprintf(`"description":"Demo"},{"id":"one","posted":%d,"amount":"2.00"}`, now.Unix()-1), 1),
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
		p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", map[string]AccountConfig{ScopedID("default", "bank", "a"): {Type: canon.AccountTypeChecking, BalanceSign: 1}})
		p.client = server.Client()
		p.now = func() time.Time { return now }
		batch, err := p.Sync(context.Background(), "")
		server.Close()
		if err == nil || batch != nil {
			t.Fatal("accepted malformed batch")
		}
	}
}

func TestSyncChunksInitialHistoryAndDiscardsPartialResult(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprint(failSecond), func(t *testing.T) {
			calls := 0
			var previousEnd int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				start, _ := strconv.ParseInt(r.URL.Query().Get("start-date"), 10, 64)
				end, _ := strconv.ParseInt(r.URL.Query().Get("end-date"), 10, 64)
				if end-start > 45*86400 {
					_, _ = fmt.Fprint(w, `{"accounts":[],"errlist":[{"code":"gen.api","msg":"Requested date range exceeds recommended range of 45 days"}]}`)
					return
				}
				if calls == 1 && start != now.Add(-90*24*time.Hour).Unix() {
					t.Error("lost initial history")
				}
				if calls == 2 && (start != previousEnd || end != now.Unix()) {
					t.Error("gap or incorrect window endpoint")
				}
				previousEnd = end
				if failSecond && calls == 2 {
					_, _ = fmt.Fprint(w, `{"accounts":[],"errlist":[{"code":"act.missingdata"}]}`)
					return
				}
				_, _ = fmt.Fprintf(w, `{"accounts":[{"conn_id":"bank","id":"a","name":"Demo","currency":"USD","balance":"20.00","balance-date":%d,"transactions":[{"id":"row-%d","posted":%d,"amount":"-1.00"},{"id":"repeated","posted":%d,"amount":"-%d.00"}]}]}`, now.Unix(), calls, start+1, start+2, calls)
			}))
			defer server.Close()
			p, _ := New(strings.Replace(server.URL, "https://", "https://user:secret@", 1), "default", map[string]AccountConfig{ScopedID("default", "bank", "a"): {Type: canon.AccountTypeChecking, BalanceSign: 1}})
			p.client = server.Client()
			p.now = func() time.Time { return now }
			batch, err := p.Sync(context.Background(), "")
			if calls != 2 {
				t.Fatalf("requests=%d want 2", calls)
			}
			if failSecond {
				if err == nil || batch != nil {
					t.Fatal("partial history advanced checkpoint")
				}
				return
			}
			if err != nil || len(batch.Accounts) != 1 || len(batch.Balances) != 1 || len(batch.Added) != 3 || batch.NextCursor != now.Format(time.RFC3339) {
				t.Fatalf("batch=%#v error=%v", batch, err)
			}
			for _, row := range batch.Added {
				if row.ProviderTxnID == ScopedID(ScopedID("default", "bank", "a"), "repeated") && row.AmountCents != -200 {
					t.Fatal("later window revision lost")
				}
			}
		})
	}
}
