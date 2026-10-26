package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUsageAndUnknownCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantText string
	}{
		{name: "help", args: []string{"help"}, wantCode: 0, wantText: "usage: moneta"},
		{name: "missing", wantCode: 2, wantText: "usage: moneta"},
		{name: "unknown", args: []string{"unknown"}, wantCode: 2, wantText: "unknown command"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Errorf("run() code = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.wantText) {
				t.Errorf("run() output = %q, want %q", stdout.String()+stderr.String(), test.wantText)
			}
		})
	}
}

func TestRunLinkRequiresDatabasePathBeforeCredentials(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"link"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

func TestRunSyncRequiresDatabasePath(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sync"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

// TestRunUsageErrorsExitTwoAcrossCommands pins the shared contract: unknown
// flags and stray positional arguments are usage errors (exit 2) on every
// command.
func TestRunSyncAcceptsResetCursorFlag(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sync", "--reset-cursor"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2 for missing database", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q, want database requirement after flag parsing", stderr.String())
	}
	if strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("--reset-cursor was rejected as unknown: %q", stderr.String())
	}
}

func TestRunUsageErrorsExitTwoAcrossCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"sync unknown flag", []string{"sync", "--bogus"}},
		{"sync positional", []string{"sync", "extra"}},
		{"link unknown flag", []string{"link", "--bogus"}},
		{"link positional", []string{"link", "extra"}},
		{"accounts unknown flag", []string{"accounts", "--bogus"}},
		{"status unknown flag", []string{"status", "--bogus"}},
		{"tx unknown flag", []string{"tx", "--bogus"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != 2 {
				t.Errorf("run(%v) code = %d, want 2 (stderr %q)", test.args, code, stderr.String())
			}
		})
	}
}

// TestRunSyncUnknownFlagPrintsErrorOnce guards against the old double-print:
// the flag package writes the parse error, so run must not repeat it.
func TestRunSyncUnknownFlagPrintsErrorOnce(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sync", "--bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if count := strings.Count(stderr.String(), "flag provided but not defined"); count != 1 {
		t.Errorf("parse error printed %d times, want 1:\n%s", count, stderr.String())
	}
	if strings.Contains(stderr.String(), "error: flag provided") {
		t.Errorf("run() re-printed the parse error with an error: prefix:\n%s", stderr.String())
	}
}

// Removed provider flags must fail before opening a database or contacting a provider.
func TestRemovedProviderRejectedWithoutSideEffects(t *testing.T) {
	for _, command := range []string{"link", "sync"} {
		t.Run(command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "untouched.db")
			var out, errout bytes.Buffer
			code := run(context.Background(), []string{command, "--db", path, "--provider", "plaid"}, &out, &errout)
			if code != 2 || !strings.Contains(errout.String(), "only SimpleFIN is supported") {
				t.Fatalf("code=%d error=%s", code, errout.String())
			}
			if out.Len() != 0 {
				t.Fatal("unexpected stdout")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("database touched: %v", err)
			}
		})
	}
}
