package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	// Embedded zone data keeps MONETA_TIMEZONE valid on hosts without system tzdata.
	_ "time/tzdata"
)

const (
	databasePathEnvironment = "MONETA_DB_PATH"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "link":
		return runLink(ctx, args[1:], stdout, stderr)
	case "sync":
		return runSync(ctx, args[1:], stdout, stderr)
	case "simplefin-accounts":
		return runSimpleFINAccounts(ctx, args[1:], stdout, stderr)
	case "configure-account":
		return runConfigureAccount(ctx, args[1:], stdout, stderr)
	case "categories":
		return runCategories(ctx, args[1:], stdout, stderr)
	case "correct-tx":
		return runCorrectTx(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "accounts":
		return runAccounts(ctx, args[1:], stdout, stderr)
	case "tx":
		return runTx(ctx, args[1:], stdout, stderr)
	case "spend":
		return runSpend(ctx, args[1:], stdout, stderr)
	case "cashflow":
		return runCashflow(ctx, args[1:], stdout, stderr)
	case "networth":
		return runNetworth(ctx, args[1:], stdout, stderr)
	case "trends":
		return runTrends(ctx, args[1:], stdout, stderr)
	case "debts":
		return runDebts(ctx, args[1:], stdout, stderr)
	case "cards":
		return runCards(ctx, args[1:], stdout, stderr)
	case "recurring":
		return runRecurring(ctx, args[1:], stdout, stderr)
	case "bills":
		return runBills(ctx, args[1:], stdout, stderr)
	case "anomalies":
		return runAnomalies(ctx, args[1:], stdout, stderr)
	case "dashboard":
		return runDashboard(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// runLink uses environment-only credentials and returns 2 for usage errors.
func runLink(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("link", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "simplefin", "provider (simplefin only)")
	name := flags.String("item", "default", "local SimpleFIN connection name")
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path (default MONETA_DB_PATH)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "error: link does not accept positional arguments")
		return 2
	}
	if !validItemName(stderr, *name) {
		return 2
	}
	if *provider != "simplefin" {
		_, _ = fmt.Fprintln(stderr, "error: only SimpleFIN is supported; existing provider data remains readable")
		return 2
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}
	return linkSimpleFIN(ctx, *path, *name, stdout, stderr)
}

// runSync imports posted SimpleFIN activity without automatic recurring
// detection. MONETA_TIMEZONE optionally selects the calendar zone for
// provider timestamps; an invalid value is a usage error.
func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "simplefin", "provider (simplefin only)")
	name := flags.String("item", "default", "local SimpleFIN connection name")
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path (default MONETA_DB_PATH)")
	reset := flags.Bool("reset-cursor", false, "replay latest 90 days from SimpleFIN")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "error: sync does not accept positional arguments")
		return 2
	}
	if !validItemName(stderr, *name) {
		return 2
	}
	if *provider != "simplefin" {
		_, _ = fmt.Fprintln(stderr, "error: only SimpleFIN is supported; existing provider data remains readable")
		return 2
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}
	location, err := simplefinLocation()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	return syncSimpleFIN(ctx, *path, *name, *reset, location, stdout, stderr)
}

func printUsage(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "usage: moneta <command>")
	_, _ = fmt.Fprintln(writer, "commands:")
	_, _ = fmt.Fprintln(writer, "  link    connect SimpleFIN using SIMPLEFIN_SETUP_TOKEN")
	_, _ = fmt.Fprintln(writer, "  sync    sync posted transactions and balances from SimpleFIN")
	_, _ = fmt.Fprintln(writer, "  simplefin-accounts  discover accounts for explicit type/sign configuration")
	_, _ = fmt.Fprintln(writer, "  configure-account  configure a discovered account before import")
	_, _ = fmt.Fprintln(writer, "  categories  list local category IDs")
	_, _ = fmt.Fprintln(writer, "  correct-tx  persist a transaction classification (use tx --review for IDs)")
	_, _ = fmt.Fprintln(writer, "  status  show linked items, health, and last-sync signals (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  accounts  list accounts with latest balances (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  tx        list transactions with an aggregate header (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  spend     summarize posted spending by category and merchant (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  cashflow  summarize posted inflow, outflow, net, and savings rate (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  networth  summarize current or daily historical net worth (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  trends    compare compute-on-read financial trends (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  debts     list credit-card and loan balances with terms (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  cards     list credit cards with limit, utilization, APR, and due day (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  recurring list detected and manual recurring series (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  bills     list recurring obligations and active card dues (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  anomalies list category-level spend spikes against prior months (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  dashboard summarize net worth, cash, credit, this month, and sync health (TOON on stdout)")
	_, _ = fmt.Fprintln(writer, "  serve     serve authenticated JSON reads over loopback HTTP")
}
