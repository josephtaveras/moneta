package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

// ErrProviderSync wraps every error returned by the provider's Sync call, so
// callers can tell provider failures from local decrypt, build, and database
// failures. It never adds provider-supplied text of its own.
var ErrProviderSync = errors.New("sync provider item")

// SyncResult summarizes one orchestrated provider Item sync.
type SyncResult struct {
	// Skipped lists records dropped during provider normalization and ingest,
	// provider records first, with stable machine-readable reasons. It is
	// empty when the sync dropped nothing.
	Skipped []canon.SkippedRecord
	// NextCursor is the checkpoint committed with this batch.
	NextCursor string
	// HasMore reports that the provider stopped short of the present. Sync
	// again with NextCursor as both the pull and expected cursor to continue.
	HasMore bool
	// Accounts and Transactions count the account and added transaction
	// records the provider returned in this batch.
	Accounts     int
	Transactions int
	// ConnectionWarnings counts provider connection warnings that did not
	// fail the batch.
	ConnectionWarnings int
}

// SyncProviderItem decrypts one stored credential, builds its provider, pulls
// from pullCursor, bootstraps the Phase 1 entity, and atomically applies the
// returned batch against expectedStoredCursor. Callers may pass an empty or
// earlier pull cursor with the loaded database cursor as the expectation to
// replay history without clearing the checkpoint. Decrypted credential bytes
// are cleared before return.
func SyncProviderItem(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	item store.ProviderItem,
	pullCursor string,
	expectedStoredCursor string,
	buildProvider func(accessToken string) (canon.Provider, error),
) (*SyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if cipher == nil {
		return nil, fmt.Errorf("secret cipher is required")
	}
	if item.DatabaseID <= 0 {
		return nil, fmt.Errorf("provider item database id must be positive")
	}
	if len(item.AccessTokenEnc) == 0 {
		return nil, fmt.Errorf("encrypted access token is required")
	}
	if buildProvider == nil {
		return nil, fmt.Errorf("provider builder is required")
	}

	plaintext, err := cipher.Open(item.AccessTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt provider item access token: %w", err)
	}
	return syncProviderItemWithPlaintext(
		ctx,
		db,
		item,
		pullCursor,
		expectedStoredCursor,
		plaintext,
		buildProvider,
	)
}

func syncProviderItemWithPlaintext(
	ctx context.Context,
	db *sql.DB,
	item store.ProviderItem,
	pullCursor string,
	expectedStoredCursor string,
	plaintext []byte,
	buildProvider func(accessToken string) (canon.Provider, error),
) (*SyncResult, error) {
	defer clear(plaintext)

	provider, err := buildProvider(string(plaintext))
	if err != nil {
		return nil, fmt.Errorf("build provider: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("provider builder returned nil")
	}
	batch, err := provider.Sync(ctx, pullCursor)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderSync, err)
	}

	defaultEntityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		return nil, err
	}
	ingestResult, err := NewIngestor(db).ApplySync(ctx, SyncTarget{
		ProviderItemID:  item.DatabaseID,
		DefaultEntityID: defaultEntityID,
		ExpectedCursor:  expectedStoredCursor,
	}, batch)
	if errors.Is(err, ErrCursorChanged) {
		return nil, ErrCursorChanged
	}
	if err != nil {
		return nil, fmt.Errorf("apply provider sync: %w", err)
	}

	result := &SyncResult{
		NextCursor:         batch.NextCursor,
		HasMore:            batch.HasMore,
		Accounts:           len(batch.Accounts),
		Transactions:       len(batch.Added),
		ConnectionWarnings: batch.ConnectionWarnings,
	}
	result.Skipped = append(result.Skipped, batch.Skipped...)
	result.Skipped = append(result.Skipped, ingestResult.Skipped...)
	return result, nil
}
