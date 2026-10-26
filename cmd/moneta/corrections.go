package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

func runCategories(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("categories", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *path == "" {
		_, _ = fmt.Fprintln(stderr, "error: database required; no positional arguments")
		return 2
	}
	db, err := store.Open(ctx, *path)
	if err != nil {
		return commandError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	categories, err := store.ListCategories(ctx, db)
	if err != nil {
		return commandError(stderr, err)
	}
	table := toon.Table{Fields: []string{"id", "name", "kind"}, Rows: [][]any{}}
	for _, c := range categories {
		table.Rows = append(table.Rows, []any{c.ID, c.Name, c.Kind})
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, toon.Object{{Key: "categories", Value: table}}, format); err != nil {
		return commandError(stderr, err)
	}
	return 0
}
func runCorrectTx(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("correct-tx", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("db", os.Getenv(databasePathEnvironment), "SQLite database path")
	id := flags.Int64("id", 0, "transaction ID from tx --review")
	category := flags.String("category", "", "category ID from categories; 0 means uncategorized")
	transfer := flags.String("transfer", "", "true or false; true also excludes the row")
	excluded := flags.String("excluded", "", "true or false")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	change := store.TransactionCorrection{}
	valid := flags.NArg() == 0 && *path != "" && *id > 0 && (*category != "" || *transfer != "" || *excluded != "")
	if *category != "" {
		n, err := strconv.ParseInt(*category, 10, 64)
		if err != nil || n < 0 {
			valid = false
		}
		change.CategoryID = &n
	}
	for _, option := range []struct {
		value  string
		target **bool
	}{{*transfer, &change.Transfer}, {*excluded, &change.Excluded}} {
		if option.value != "" {
			if option.value != "true" && option.value != "false" {
				valid = false
			}
			v := option.value == "true"
			*option.target = &v
		}
	}
	if change.Transfer != nil && *change.Transfer && change.Excluded != nil && !*change.Excluded {
		valid = false
	}
	if !valid {
		_, _ = fmt.Fprintln(stderr, "error: require database, positive --id and --category ID, --transfer true|false, or --excluded true|false")
		return 2
	}
	db, err := store.Open(ctx, *path)
	if err != nil {
		return commandError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	if err := store.CorrectTransaction(ctx, db, *id, change); err != nil {
		return commandError(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, "Classification saved and protected from re-import. For transfers, correct the other leg separately.")
	return 0
}
func buildTxReviewDoc(rows []store.TransactionRow) toon.Object {
	table := toon.Table{Fields: []string{"id", "date", "amount", "merchant", "account", "institution", "category_id", "transfer", "excluded", "manual"}, Rows: [][]any{}}
	for _, r := range rows {
		var category any
		if r.CategoryID != nil {
			category = *r.CategoryID
		}
		table.Rows = append(table.Rows, []any{r.ID, r.Date, cli.Money(r.AmountCents), r.Merchant, r.AccountName, r.Institution, category, r.Transfer, r.Excluded, r.Manual})
	}
	return toon.Object{{Key: "review", Value: table}, {Key: "hint", Value: "correct-tx --id ID --category ID or --transfer true; categories lists category IDs; use --full to review all matching rows"}}
}
