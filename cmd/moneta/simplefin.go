package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/core"
	"github.com/jmoneytech-stack/moneta/internal/providers/simplefin"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

const (
	simplefinProviderName = "simplefin"
	timezoneEnvironment   = "MONETA_TIMEZONE"
	// simplefinMaxPasses bounds one sync invocation to at most 20 Bridge
	// requests (two per pass), under the guide's 24 requests per day.
	simplefinMaxPasses = 10
	// simplefinLookbackLimit bounds how far before the stored checkpoint a
	// sync reaches back after connection warnings.
	simplefinLookbackLimit = 90 * 24 * time.Hour
)

// validItemName rejects a blank --item as a usage error, so every SimpleFIN
// command resolves the same explicit local connection name.
func validItemName(stderr io.Writer, name string) bool {
	if strings.TrimSpace(name) != "" {
		return true
	}
	_, _ = fmt.Fprintln(stderr, "error: --item must name a local SimpleFIN connection and must not be blank")
	return false
}

// simplefinLocation returns the zone that turns provider timestamps into
// calendar dates: UTC unless MONETA_TIMEZONE names an IANA time zone.
func simplefinLocation() (*time.Location, error) {
	name := os.Getenv(timezoneEnvironment)
	if name == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, fmt.Errorf("%s=%q is not an IANA time zone name such as America/New_York; unset it to use UTC", timezoneEnvironment, name)
	}
	return location, nil
}

func linkSimpleFIN(ctx context.Context, path, name string, stdout, stderr io.Writer) int {
	cipher, err := secret.FromEnvironment()
	if err != nil {
		return commandError(stderr, err)
	}
	token := os.Getenv("SIMPLEFIN_SETUP_TOKEN")
	if token == "" {
		_, _ = fmt.Fprintln(stderr, "error: SIMPLEFIN_SETUP_TOKEN is required; supply it through the environment, never an argument")
		return 2
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		return commandError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	access, err := simplefin.Claim(ctx, token)
	if err != nil {
		return commandError(stderr, err)
	}
	encrypted, err := cipher.Seal([]byte(access))
	if err != nil {
		return commandError(stderr, err)
	}
	_, err = store.SaveProviderItem(ctx, db, store.ProviderItemSecret{Provider: simplefinProviderName, ItemID: name, Institution: "SimpleFIN", AccessTokenCiphertext: encrypted})
	if err != nil {
		return commandError(stderr, errors.New("setup token was claimed but credential storage failed; revoke it in Bridge and create a new token"))
	}
	_, _ = fmt.Fprintln(stdout, "SimpleFIN linked. Run moneta simplefin-accounts, configure every account, then moneta sync.")
	return 0
}
func commandError(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
	return 1
}

// simplefinConnection is what networked SimpleFIN commands load once: the
// database, cipher, named connection, explicit account decisions, and the
// calendar zone for provider timestamps.
type simplefinConnection struct {
	db       *sql.DB
	cipher   *secret.Cipher
	item     store.ProviderItem
	config   map[string]simplefin.AccountConfig
	location *time.Location
}

func openSimpleFIN(ctx context.Context, path, name string, location *time.Location) (*simplefinConnection, error) {
	cipher, err := secret.FromEnvironment()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	item, err := store.GetProviderItem(ctx, db, simplefinProviderName, name)
	if err != nil {
		_ = db.Close()
		return nil, errors.New("SimpleFIN connection not found; run moneta link first")
	}
	rows, err := store.ListSimpleFINAccounts(ctx, db, item.DatabaseID)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	config := map[string]simplefin.AccountConfig{}
	for _, row := range rows {
		if row.Type != "" || row.Ignored {
			config[row.ID] = simplefin.AccountConfig{Type: canon.AccountType(row.Type), BalanceSign: row.BalanceSign, Ignore: row.Ignored}
		}
	}
	return &simplefinConnection{db: db, cipher: cipher, item: item, config: config, location: location}, nil
}

func (c *simplefinConnection) newProvider(accessURL string) (*simplefin.Provider, error) {
	provider, err := simplefin.New(accessURL, c.item.ItemID, c.config)
	if err != nil {
		return nil, err
	}
	provider.SetLocation(c.location)
	return provider, nil
}

// buildProvider is the core.SyncProviderItem builder; core clears the
// decrypted Access URL bytes after the sync.
func (c *simplefinConnection) buildProvider(accessURL string) (canon.Provider, error) {
	provider, err := c.newProvider(accessURL)
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// provider decrypts the Access URL for a direct request such as Discover and
// clears the decrypted bytes before returning.
func (c *simplefinConnection) provider() (*simplefin.Provider, error) {
	plain, err := c.cipher.Open(c.item.AccessTokenEnc)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return c.newProvider(string(plain))
}

// simplefinFailure reports err and persists connection health only when the
// failure came from a Bridge request. Local configuration, checkpoint,
// cursor, database, and interrupt failures leave stored health unchanged.
func simplefinFailure(ctx context.Context, db *sql.DB, item store.ProviderItem, err error, fromBridge bool, stderr io.Writer) int {
	if fromBridge && ctx.Err() == nil && !errors.Is(err, simplefin.ErrLocalConfiguration) {
		status := "error"
		if errors.Is(err, simplefin.ErrAuthentication) {
			status = "login_required"
		}
		if statusErr := store.SetProviderItemStatus(ctx, db, simplefinProviderName, item.ItemID, status); statusErr != nil {
			_, _ = fmt.Fprintln(stderr, "error: could not persist SimpleFIN connection health")
		}
	}
	return commandError(stderr, err)
}

// simplefinPullCursor returns the cursor a sync pulls from. Connection
// warnings do not stop the checkpoint from advancing, but a warning
// institution can add rows late, behind it. So a sync pulls from the last
// checkpoint committed without warnings when that is earlier than the stored
// checkpoint, or from simplefinLookbackLimit before the stored checkpoint when
// there is no such checkpoint or it is older than that.
func simplefinPullCursor(stored, clean string) string {
	checkpoint, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		// An empty checkpoint is a first sync; the provider rejects an
		// invalid one as a local configuration problem.
		return stored
	}
	pull := checkpoint.Add(-simplefinLookbackLimit)
	if cleanAt, err := time.Parse(time.RFC3339, clean); err == nil && cleanAt.After(pull) {
		pull = cleanAt
	}
	if !pull.Before(checkpoint) {
		return stored
	}
	return pull.UTC().Format(time.RFC3339)
}

// syncSimpleFIN commits one batch per pass through core.SyncProviderItem and
// continues from each committed checkpoint until the provider is caught up.
// Each pass reports its own counts and warnings as soon as it commits.
func syncSimpleFIN(ctx context.Context, path, name string, reset bool, location *time.Location, stdout, stderr io.Writer) int {
	connection, err := openSimpleFIN(ctx, path, name, location)
	if err != nil {
		return commandError(stderr, err)
	}
	defer func() { _ = connection.db.Close() }()
	// The stored checkpoint stays the compare-and-swap expectation even when
	// the first pass pulls from an earlier cursor. Reset pulls the latest 90
	// days.
	expected := connection.item.SyncCursor
	pull := ""
	if !reset {
		clean, err := store.LastCleanSyncCursor(ctx, connection.db, connection.item.DatabaseID)
		if err != nil {
			return commandError(stderr, err)
		}
		pull = simplefinPullCursor(expected, clean)
		if pull != expected {
			_, _ = fmt.Fprintf(stdout, "Rechecking from %s because syncs since then reported connection warnings.\n", pull)
		}
	}
	passes := 0
	for {
		result, err := core.SyncProviderItem(ctx, connection.db, connection.cipher, connection.item, pull, expected, connection.buildProvider)
		if err != nil {
			if passes > 0 {
				_, _ = fmt.Fprintf(stderr, "note: %d earlier sync pass(es) committed through checkpoint %s; run moneta sync again to continue\n", passes, expected)
			}
			return simplefinFailure(ctx, connection.db, connection.item, err, errors.Is(err, core.ErrProviderSync), stderr)
		}
		passes++
		_, _ = fmt.Fprintf(stdout, "SimpleFIN pass %d: %d accounts, %d posted records received, %d skipped, %d connection warnings. Checkpoint %s.\n", passes, result.Accounts, result.Transactions, len(result.Skipped), result.ConnectionWarnings, result.NextCursor)
		if result.ConnectionWarnings > 0 {
			_, _ = fmt.Fprintf(stderr, "warning: SimpleFIN pass %d reported %d connection warning(s); some institutions may be stale; check the Bridge connection page; later syncs automatically reach back to the last warning-free sync, up to 90 days, to pick up late rows\n", passes, result.ConnectionWarnings)
		}
		pull, expected = result.NextCursor, result.NextCursor
		if !result.HasMore {
			break
		}
		if passes == simplefinMaxPasses {
			_, _ = fmt.Fprintf(stderr, "error: checkpoint is still behind after %d passes; progress through checkpoint %s is kept; run moneta sync again tomorrow, after the Bridge daily request budget resets\n", passes, expected)
			return 1
		}
	}
	if passes > 1 {
		_, _ = fmt.Fprintf(stdout, "Caught up from an older checkpoint in %d passes.\n", passes)
	}
	_, _ = fmt.Fprintln(stdout, "Review transfers with moneta tx --review. Advanced detection is paused until reconciliation.")
	return 0
}

func runSimpleFINAccounts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("simplefin-accounts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path")
	item := flags.String("item", "default", "local SimpleFIN connection name")
	cached := flags.Bool("cached", false, "show saved discovery without a network request or encryption key")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *path == "" {
		_, _ = fmt.Fprintln(stderr, "error: --db or MONETA_DB_PATH required; no positional arguments")
		return 2
	}
	if !validItemName(stderr, *item) {
		return 2
	}
	var location *time.Location
	if !*cached {
		var err error
		if location, err = simplefinLocation(); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
	}
	var db *sql.DB
	var connection store.ProviderItem
	var err error
	if *cached {
		db, err = store.Open(ctx, *path)
		if err != nil {
			return commandError(stderr, err)
		}
		defer func() { _ = db.Close() }()
		connection, err = store.GetProviderItem(ctx, db, simplefinProviderName, *item)
		if err != nil {
			return commandError(stderr, err)
		}
	} else {
		opened, err := openSimpleFIN(ctx, *path, *item, location)
		if err != nil {
			return commandError(stderr, err)
		}
		db, connection = opened.db, opened.item
		defer func() { _ = db.Close() }()
		provider, err := opened.provider()
		if err != nil {
			return commandError(stderr, err)
		}
		discovered, err := provider.Discover(ctx)
		if err != nil {
			return simplefinFailure(ctx, db, connection, err, true, stderr)
		}
		// A successful Bridge request proves the connection is healthy again.
		if err := store.SetProviderItemStatus(ctx, db, simplefinProviderName, connection.ItemID, "ok"); err != nil {
			return commandError(stderr, err)
		}
		accounts := make([]store.SimpleFINAccount, 0, len(discovered))
		for _, a := range discovered {
			accounts = append(accounts, store.SimpleFINAccount{ID: a.ID, Name: a.Name, Institution: a.Institution, Currency: a.Currency, BalanceCents: a.BalanceCents, BalanceDate: a.BalanceDate})
		}
		if err := store.SaveSimpleFINDiscovery(ctx, db, connection.DatabaseID, accounts); err != nil {
			return commandError(stderr, err)
		}
	}
	accounts, err := store.ListSimpleFINAccounts(ctx, db, connection.DatabaseID)
	if err != nil {
		return commandError(stderr, err)
	}
	table := toon.Table{Fields: []string{"id", "name", "institution", "currency", "source_balance", "balance_date", "type", "balance_sign", "ignored"}, Rows: [][]any{}}
	for _, a := range accounts {
		var money any
		if a.BalanceCents != nil {
			money = cli.Money(*a.BalanceCents)
		}
		table.Rows = append(table.Rows, []any{a.ID, a.Name, a.Institution, a.Currency, money, a.BalanceDate, a.Type, a.BalanceSign, a.Ignored})
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, toon.Object{{Key: "accounts", Value: table}, {Key: "hint", Value: "configure-account --account ID --type TYPE --balance-sign 1|-1; use --ignore for accounts outside scope"}}, format); err != nil {
		return commandError(stderr, err)
	}
	return 0
}
func runConfigureAccount(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("configure-account", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path")
	item := flags.String("item", "default", "local SimpleFIN connection name")
	account := flags.String("account", "", "ID from simplefin-accounts")
	kind := flags.String("type", "", "checking|savings|credit_card|loan|investment|asset")
	sign := flags.Int64("balance-sign", 0, "multiply source current balance by 1 or -1; owed debt must be positive")
	ignore := flags.Bool("ignore", false, "explicitly omit this account from imports")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *path == "" || *account == "" || (!*ignore && (!validCLAccountType(*kind) || (*sign != 1 && *sign != -1))) || (*ignore && (*kind != "" || *sign != 0)) {
		_, _ = fmt.Fprintln(stderr, "error: require database, --account and either --type with --balance-sign 1|-1, or --ignore")
		return 2
	}
	if !validItemName(stderr, *item) {
		return 2
	}
	db, err := store.Open(ctx, *path)
	if err != nil {
		return commandError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	connection, err := store.GetProviderItem(ctx, db, simplefinProviderName, *item)
	if err != nil {
		return commandError(stderr, err)
	}
	if err := store.ConfigureSimpleFINAccount(ctx, db, connection.DatabaseID, *account, *kind, *sign, *ignore); err != nil {
		return commandError(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, "Account configuration saved.")
	return 0
}
