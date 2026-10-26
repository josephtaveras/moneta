package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// updatePayloadFixtures rewrites the committed sample payloads instead of
// asserting against them: go test ./cmd/moneta -run PayloadFixtures
// -update-payload-fixtures.
var updatePayloadFixtures = flag.Bool(
	"update-payload-fixtures",
	false,
	"rewrite the committed tx payload fixtures in testdata",
)

// payloadFixtureRows is the sample size the committed fixtures and the
// headline token-reduction figure in README.md both use.
const payloadFixtureRows = 500

// benchMerchants and benchAccounts are neutral synthetic values. No real
// merchant, institution, or account name ever enters this repository.
var (
	benchMerchants = []string{
		"Grocery Mart",
		"Coffee Place",
		"Stream Co",
		"Transit Authority",
		"Corner Pharmacy",
		"Fuel Stop",
		"Hardware Depot",
		"Employer Inc",
		"Utility Co",
		"Book Shop",
	}
	benchAccounts = []string{"Everyday Checking", "Travel Card"}
)

// benchTransactionRows builds count deterministic rows in the shape
// store.ListTransactions returns, newest first. Deterministic so the
// committed fixtures are reproducible on any machine.
func benchTransactionRows(count int) []store.TransactionRow {
	start := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	rows := make([]store.TransactionRow, 0, count)
	for i := range count {
		amount := int64(-125 - (i*997)%48_500)
		if i%23 == 0 {
			amount = int64(180_000 + (i*311)%40_000) // periodic inflow
		}
		status := "posted"
		if i < 3 {
			status = "pending"
		}
		rows = append(rows, store.TransactionRow{
			ID:          int64(i + 1),
			Date:        start.AddDate(0, 0, -(i / 6)).Format("2006-01-02"),
			AmountCents: amount,
			Merchant:    benchMerchants[i%len(benchMerchants)],
			Status:      status,
			AccountName: benchAccounts[i%len(benchAccounts)],
		})
	}
	return rows
}

// benchTransactionSummary aggregates rows the way store.SummarizeTransactions
// does for a filter that excludes nothing, so the benchmarked document
// carries a realistic summary block.
func benchTransactionSummary(rows []store.TransactionRow) store.TransactionSummary {
	summary := store.TransactionSummary{Count: len(rows)}
	for _, row := range rows {
		summary.TotalCents += row.AmountCents
		if row.AmountCents >= 0 {
			summary.InflowCents += row.AmountCents
			continue
		}
		summary.OutflowCents += row.AmountCents
	}
	return summary
}

// benchTxDoc builds the real `moneta tx --full` document through the
// production builder, so the payload measured here is the payload shipped.
func benchTxDoc(count int) toon.Object {
	rows := benchTransactionRows(count)
	return buildTxDoc(benchTransactionSummary(rows), rows, store.TransactionFilter{})
}

func renderPayload(tb testing.TB, doc toon.Object, format cli.Format) []byte {
	tb.Helper()

	var buffer bytes.Buffer
	if err := cli.Render(&buffer, doc, format); err != nil {
		tb.Fatalf("cli.Render() error: %v", err)
	}
	return buffer.Bytes()
}

// BenchmarkRenderTx measures encode throughput and payload size for the same
// tx document in both output formats. The JSON arm is cli.FormatJSON, which
// is compact single-line JSON: the smallest reasonable JSON form, so the
// TOON reduction reported here is the conservative one.
func BenchmarkRenderTx(b *testing.B) {
	for _, rows := range []int{50, payloadFixtureRows, 2000} {
		doc := benchTxDoc(rows)
		for _, format := range []struct {
			name  string
			value cli.Format
		}{
			{"toon", cli.FormatTOON},
			{"json", cli.FormatJSON},
		} {
			b.Run(fmt.Sprintf("%s/rows=%d", format.name, rows), func(b *testing.B) {
				size := len(renderPayload(b, doc, format.value))
				b.SetBytes(int64(size))
				b.ReportMetric(float64(size), "B/payload")
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					var sink bytes.Buffer
					if err := cli.Render(&sink, doc, format.value); err != nil {
						b.Fatalf("cli.Render() error: %v", err)
					}
				}
			})
		}
	}
}

// TestTOONPayloadSmallerThanJSON locks the byte-level size claim. Tokens are
// counted out of band (scripts/token_compare.py) because a tokenizer would
// mean a new dependency; bytes are the dependency-free proxy that CI can
// enforce, and the two move together for this payload shape.
func TestTOONPayloadSmallerThanJSON(t *testing.T) {
	doc := benchTxDoc(payloadFixtureRows)
	toonSize := len(renderPayload(t, doc, cli.FormatTOON))
	jsonSize := len(renderPayload(t, doc, cli.FormatJSON))

	if toonSize >= jsonSize {
		t.Fatalf("TOON payload = %d bytes, JSON = %d bytes; want TOON smaller", toonSize, jsonSize)
	}
	reduction := 100 * float64(jsonSize-toonSize) / float64(jsonSize)
	// Floor, not the measured value: this fails if a change erodes the
	// documented saving, without churning on every small shape tweak.
	const minReduction = 40.0
	if reduction < minReduction {
		t.Errorf(
			"TOON reduction = %.1f%% (%d vs %d bytes), want at least %.0f%%",
			reduction, toonSize, jsonSize, minReduction,
		)
	}
	t.Logf("rows=%d toon=%dB json=%dB reduction=%.1f%%",
		payloadFixtureRows, toonSize, jsonSize, reduction)
}

// TestPayloadFixtures keeps the committed sample payloads in step with the
// document builder, so the token figures in README.md are always derived
// from output the current code actually produces.
func TestPayloadFixtures(t *testing.T) {
	doc := benchTxDoc(payloadFixtureRows)
	fixtures := []struct {
		name   string
		format cli.Format
		indent bool
	}{
		{fmt.Sprintf("tx_sample_%d.toon", payloadFixtureRows), cli.FormatTOON, false},
		{fmt.Sprintf("tx_sample_%d.json", payloadFixtureRows), cli.FormatJSON, false},
		// Indented JSON is not an output format Moneta offers; it is the
		// fixture for the secondary comparison against the pretty-printed
		// JSON most REST APIs return. json.Indent reshapes the exact bytes
		// cli.Render produced, so no number is reformatted.
		{fmt.Sprintf("tx_sample_%d.pretty.json", payloadFixtureRows), cli.FormatJSON, true},
	}

	for _, fixture := range fixtures {
		path := filepath.Join("testdata", fixture.name)
		want := renderPayload(t, doc, fixture.format)
		if fixture.indent {
			var indented bytes.Buffer
			if err := json.Indent(&indented, want, "", "  "); err != nil {
				t.Fatalf("indent %s: %v", fixture.name, err)
			}
			want = indented.Bytes()
		}
		if *updatePayloadFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write fixture %s: %v", path, err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture %s (regenerate with -update-payload-fixtures): %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf(
				"fixture %s is stale (%d bytes on disk, %d generated); "+
					"regenerate with -update-payload-fixtures",
				fixture.name, len(got), len(want),
			)
		}
	}
}
