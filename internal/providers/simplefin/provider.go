// Package simplefin implements posted-only SimpleFIN v2 ingestion.
package simplefin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

var ErrAuthentication = errors.New("SimpleFIN authentication required; reconnect through Bridge")

// ErrLocalConfiguration marks failures caused by local account configuration
// or the stored sync checkpoint rather than by Bridge. They say nothing about
// connection health. Match it with errors.Is.
var ErrLocalConfiguration = errors.New("local SimpleFIN configuration problem")

// localError keeps a specific static message while matching
// ErrLocalConfiguration.
type localError string

func (e localError) Error() string        { return string(e) }
func (e localError) Is(target error) bool { return target == ErrLocalConfiguration }

var (
	errInvalidAmount   = errors.New("invalid SimpleFIN USD amount")
	errFractionalCents = errors.New("SimpleFIN USD amount has fractional cents")
)

// AccountConfig is an explicit local decision, never inferred from a name.
type AccountConfig struct {
	Type        canon.AccountType
	BalanceSign int64
	Ignore      bool
}

type Account struct {
	ID           string        `json:"id"`
	ConnectionID string        `json:"conn_id"`
	Name         string        `json:"name"`
	Currency     string        `json:"currency"`
	Balance      string        `json:"balance"`
	Available    *string       `json:"available-balance"`
	BalanceDate  int64         `json:"balance-date"`
	Transactions []Transaction `json:"transactions"`
}
type Transaction struct {
	ID          string `json:"id"`
	Posted      int64  `json:"posted"`
	Amount      string `json:"amount"`
	Description string `json:"description"`
	Pending     bool   `json:"pending"`
}
type accountSet struct {
	Accounts    *[]Account `json:"accounts"`
	Connections []struct {
		ID   string `json:"conn_id"`
		Name string `json:"name"`
	} `json:"connections"`
	Errors    []string `json:"errors"`
	ErrorList []struct {
		Code string `json:"code"`
	} `json:"errlist"`
}

type DiscoveredAccount struct {
	ID           string
	Name         string
	Institution  string
	Currency     string
	BalanceCents *int64
	BalanceDate  string
}

type Provider struct {
	accessURL  string
	connection string
	config     map[string]AccountConfig
	client     *http.Client
	now        func() time.Time
	location   *time.Location
}

func New(accessURL, connection string, config map[string]AccountConfig) (*Provider, error) {
	if _, err := validateURL(accessURL, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(connection) == "" {
		return nil, errors.New("SimpleFIN connection name is required")
	}
	return &Provider{accessURL: accessURL, connection: connection, config: config, client: &http.Client{Timeout: 60 * time.Second}, now: time.Now, location: time.UTC}, nil
}

// SetLocation selects the time zone that turns provider timestamps into
// posted and balance calendar dates. The default is UTC; nil restores UTC.
func (p *Provider) SetLocation(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	p.location = location
}

func (p *Provider) Name() string { return "simplefin" }
func (p *Provider) Capabilities() canon.Capability {
	return canon.CapAccounts | canon.CapTransactions | canon.CapBalances
}
func (p *Provider) Connections(context.Context) ([]canon.ConnectionStatus, error) {
	return []canon.ConnectionStatus{{ID: p.connection, State: "unknown"}}, nil
}

// Claim exchanges a one-use setup token. No request URL, response body, or
// underlying HTTP error may escape through errors, because URLs are secrets.
func Claim(ctx context.Context, token string) (string, error) {
	return claim(ctx, token, &http.Client{Timeout: 60 * time.Second})
}
func claim(ctx context.Context, token string, client *http.Client) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", errors.New("invalid SimpleFIN setup token")
	}
	u, err := validateURL(string(raw), false)
	if err != nil {
		return "", errors.New("setup token must contain an HTTPS claim URL")
	}
	body, err := request(ctx, client, http.MethodPost, u, 8192)
	if err != nil {
		return "", fmt.Errorf("claim setup token: %w; if already claimed or unexpectedly rejected, revoke it in Bridge and create a new token", err)
	}
	access := strings.TrimSpace(string(body))
	if _, err := validateURL(access, true); err != nil {
		return "", errors.New("claim returned an invalid Access URL; create a new setup token")
	}
	return access, nil
}
func validateURL(value string, credentials bool) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" {
		return nil, errors.New("SimpleFIN requires a valid HTTPS URL without query or fragment")
	}
	if credentials {
		if u.User == nil {
			return nil, errors.New("SimpleFIN Access URL requires credentials")
		}
		password, ok := u.User.Password()
		if !ok || password == "" || u.User.Username() == "" {
			return nil, errors.New("SimpleFIN Access URL requires credentials")
		}
	} else if u.User != nil {
		return nil, errors.New("claim URL must not contain credentials")
	}
	return u, nil
}
func request(ctx context.Context, client *http.Client, method string, u *url.URL, limit int64) ([]byte, error) {
	target := *u
	credential := target.User
	target.User = nil
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, errors.New("cannot construct SimpleFIN request")
	}
	if credential != nil {
		password, _ := credential.Password()
		req.SetBasicAuth(credential.Username(), password)
	}
	req.Header.Set("Accept", "application/json")
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return nil, errors.New("SimpleFIN request failed; check connectivity and retry")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuthentication
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SimpleFIN HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("SimpleFIN response unreadable or too large")
	}
	return body, nil
}

func (p *Provider) fetch(ctx context.Context, values url.Values) (accountSet, error) {
	var data accountSet
	u, err := validateURL(p.accessURL, true)
	if err != nil {
		return data, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/accounts"
	u.RawPath = ""
	values.Set("version", "2")
	u.RawQuery = values.Encode()
	body, err := request(ctx, p.client, http.MethodGet, u, 32<<20)
	if err != nil {
		return data, err
	}
	if err = json.Unmarshal(body, &data); err != nil {
		return data, errors.New("invalid SimpleFIN account response")
	}
	for _, item := range data.ErrorList {
		if item.Code == "gen.auth" || item.Code == "con.auth" {
			return data, ErrAuthentication
		}
	}
	for _, item := range data.ErrorList {
		// Only known constant labels reach errors, never server-supplied messages.
		switch item.Code {
		case "gen.api":
			return data, errors.New("SimpleFIN provider API error (gen.api); request rejected; nothing imported")
		case "act.missingdata":
			return data, errors.New("SimpleFIN incomplete transaction history (act.missingdata); nothing imported")
		case "act.failed":
			return data, errors.New("SimpleFIN account retrieval failed (act.failed); nothing imported")
		}
	}
	// Any other errlist code is unrecognized and still aborts. The legacy
	// errors array carries informational per-connection messages; Sync only
	// counts them as warnings and never surfaces their text.
	if len(data.ErrorList) > 0 {
		return data, errors.New("SimpleFIN reported incomplete data or a provider error; check Bridge connection errors; nothing imported")
	}
	if data.Accounts == nil {
		return data, errors.New("SimpleFIN response is missing accounts")
	}
	seen := map[string]bool{}
	for _, a := range *data.Accounts {
		id := p.accountID(a)
		if a.ID == "" || a.ConnectionID == "" || strings.TrimSpace(a.Name) == "" || a.Currency == "" || seen[id] {
			return data, errors.New("SimpleFIN returned invalid or duplicate account identity; v2 conn_id is required")
		}
		seen[id] = true
	}
	return data, nil
}

// ScopedID uses unambiguous JSON tuple encoding before hashing opaque IDs.
func ScopedID(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p *Provider) accountID(a Account) string { return ScopedID(p.connection, a.ConnectionID, a.ID) }

func (p *Provider) Discover(ctx context.Context) ([]DiscoveredAccount, error) {
	data, err := p.fetch(ctx, url.Values{"balances-only": {"1"}})
	if err != nil {
		return nil, err
	}
	institutions := map[string]string{}
	for _, c := range data.Connections {
		institutions[c.ID] = c.Name
	}
	result := make([]DiscoveredAccount, 0, len(*data.Accounts))
	for _, a := range *data.Accounts {
		row := DiscoveredAccount{ID: p.accountID(a), Name: a.Name, Institution: institutions[a.ConnectionID], Currency: a.Currency}
		if a.Currency == "USD" {
			// A balance Money rejects stays unknown rather than blocking discovery.
			if value, err := Money(a.Balance); err == nil {
				row.BalanceCents = &value
				row.BalanceDate, err = calendarDate(a.BalanceDate, p.location)
				if err != nil {
					return nil, err
				}
			}
		}
		result = append(result, row)
	}
	return result, nil
}

// Sync retrieves at most 90 days in at most two 45-day requests. Bridge can
// warn on larger requests even though its guide documents a 90-day limit.
// When an old checkpoint needs more than 90 days, the batch stops early with
// HasMore set; callers apply it and sync again from NextCursor.
// All windows must succeed before any batch or checkpoint is returned.
// Missing transactions never imply deletions; pending rows are ignored.
func (p *Provider) Sync(ctx context.Context, cursor string) (*canon.SyncBatch, error) {
	end := p.now().UTC().Truncate(time.Second)
	start := end.Add(-90 * 24 * time.Hour)
	capped := false
	if cursor != "" {
		checkpoint, err := time.Parse(time.RFC3339, cursor)
		if err != nil || checkpoint.After(end) {
			return nil, localError("invalid SimpleFIN sync checkpoint")
		}
		start = checkpoint.Add(-5 * 24 * time.Hour)
		if limit := start.Add(90 * 24 * time.Hour); limit.Before(end) {
			end = limit
			capped = true
		}
	}
	batch := &canon.SyncBatch{NextCursor: end.Format(time.RFC3339), HasMore: capped}
	type skipKey struct {
		kind canon.RecordKind
		id   string
	}
	accountAt := map[string]int{}
	balanceAt := map[string]int{}
	transactionAt := map[string]int{}
	skipped := map[skipKey]bool{}
	warnings := map[string]bool{}
	for start.Before(end) {
		windowEnd := start.Add(45 * 24 * time.Hour)
		if windowEnd.After(end) {
			windowEnd = end
		}
		data, err := p.fetch(ctx, url.Values{"start-date": {strconv.FormatInt(start.Unix(), 10)}, "end-date": {strconv.FormatInt(windowEnd.Unix(), 10)}})
		if err != nil {
			return nil, err
		}
		for _, message := range data.Errors {
			warnings[message] = true
		}
		window, err := p.normalize(data)
		if err != nil {
			return nil, err
		}
		for _, account := range window.Accounts {
			if index, exists := accountAt[account.ProviderAccountID]; exists {
				batch.Accounts[index] = account
			} else {
				accountAt[account.ProviderAccountID] = len(batch.Accounts)
				batch.Accounts = append(batch.Accounts, account)
			}
		}
		for _, balance := range window.Balances {
			if index, exists := balanceAt[balance.AccountRef]; exists {
				if balance.Date >= batch.Balances[index].Date {
					batch.Balances[index] = balance
				}
			} else {
				balanceAt[balance.AccountRef] = len(batch.Balances)
				batch.Balances = append(batch.Balances, balance)
			}
		}
		for _, transaction := range window.Added {
			if index, exists := transactionAt[transaction.ProviderTxnID]; exists {
				batch.Added[index] = transaction
			} else {
				transactionAt[transaction.ProviderTxnID] = len(batch.Added)
				batch.Added = append(batch.Added, transaction)
			}
		}
		for _, record := range window.Skipped {
			if key := (skipKey{record.Kind, record.ID}); !skipped[key] {
				skipped[key] = true
				batch.Skipped = append(batch.Skipped, record)
			}
		}
		start = windowEnd
	}
	batch.ConnectionWarnings = len(warnings)
	return batch, nil
}

func (p *Provider) normalize(data accountSet) (*canon.SyncBatch, error) {
	batch := &canon.SyncBatch{}
	institutions := map[string]string{}
	for _, c := range data.Connections {
		institutions[c.ID] = c.Name
	}
	seenTx := map[string]bool{}
	for _, a := range *data.Accounts {
		id := p.accountID(a)
		config, ok := p.config[id]
		if !ok {
			return nil, localError("unconfigured SimpleFIN account; run simplefin-accounts and configure-account before sync")
		}
		if config.Ignore {
			continue
		}
		if a.Currency != "USD" {
			return nil, localError("unsupported currency; explicitly ignore this account before sync")
		}
		switch config.Type {
		case canon.AccountTypeChecking, canon.AccountTypeSavings, canon.AccountTypeCreditCard, canon.AccountTypeLoan, canon.AccountTypeInvestment, canon.AccountTypeAsset:
		default:
			return nil, localError("invalid SimpleFIN account type")
		}
		if config.BalanceSign != 1 && config.BalanceSign != -1 {
			return nil, localError("explicit balance sign must be 1 or -1")
		}
		date, err := calendarDate(a.BalanceDate, p.location)
		if err != nil {
			return nil, err
		}
		batch.Accounts = append(batch.Accounts, canon.Account{ProviderAccountID: id, Name: a.Name, Institution: institutions[a.ConnectionID], Type: config.Type, Currency: "USD"})
		// A current balance Money rejects skips only this balance record; the
		// account and its transactions still import. An available balance Money
		// rejects is dropped on its own and the current balance is kept, since
		// net worth depends on the current value. Available credit is not a
		// signed liability balance, so its source value is preserved.
		balance, err := Money(a.Balance)
		if err != nil {
			batch.Skipped = append(batch.Skipped, amountSkip(canon.RecordKindBalance, id, err))
		} else {
			var available *int64
			if a.Available != nil {
				if v, err := Money(*a.Available); err != nil {
					skip := amountSkip(canon.RecordKindBalance, id, err)
					skip.Detail = "available_" + skip.Detail
					batch.Skipped = append(batch.Skipped, skip)
				} else {
					available = &v
				}
			}
			batch.Balances = append(batch.Balances, canon.Balance{AccountRef: id, Date: canon.Date(date), CurrentCents: balance * config.BalanceSign, AvailableCents: available})
		}
		for _, t := range a.Transactions {
			if t.Pending {
				continue
			}
			if t.ID == "" {
				return nil, errors.New("SimpleFIN transaction ID is missing")
			}
			txID := ScopedID(id, t.ID)
			if seenTx[txID] {
				return nil, errors.New("duplicate SimpleFIN transaction ID in response")
			}
			seenTx[txID] = true
			date, err := calendarDate(t.Posted, p.location)
			if err != nil {
				return nil, err
			}
			amount, err := Money(t.Amount)
			if err != nil {
				batch.Skipped = append(batch.Skipped, amountSkip(canon.RecordKindTransaction, txID, err))
				continue
			}
			batch.Added = append(batch.Added, canon.Transaction{ProviderTxnID: txID, AccountRef: id, Date: canon.Date(date), AmountCents: amount, MerchantRaw: t.Description, Status: canon.TxnStatusPosted, Currency: "USD"})
		}
	}
	return batch, nil
}

// amountSkip records one row whose amount Money rejected. It keeps only the
// opaque ID and a static detail, never the amount.
func amountSkip(kind canon.RecordKind, id string, err error) canon.SkippedRecord {
	detail := "invalid_amount"
	if errors.Is(err, errFractionalCents) {
		detail = "fractional_cents"
	}
	return canon.SkippedRecord{Kind: kind, ID: id, Reason: canon.SkipMalformedRecord, Detail: detail}
}

// calendarDate converts a provider epoch to a calendar date in location.
func calendarDate(epoch int64, location *time.Location) (string, error) {
	if epoch <= 0 || epoch > 253402300799 {
		return "", errors.New("invalid SimpleFIN timestamp")
	}
	return time.Unix(epoch, 0).In(location).Format(time.DateOnly), nil
}

// Money parses decimal strings exactly, rejecting fractional cents and values
// that cannot be safely negated by the analytics layer. Never use float64.
func Money(value string) (int64, error) {
	invalid := errInvalidAmount
	if value == "" || len(value) > 64 {
		return 0, invalid
	}
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	whole, fraction, hasDot := strings.Cut(value, ".")
	if whole == "" || (hasDot && fraction == "") {
		return 0, invalid
	}
	for _, c := range whole + fraction {
		if c < '0' || c > '9' {
			return 0, invalid
		}
	}
	if len(fraction) > 2 {
		if strings.Trim(fraction[2:], "0") != "" {
			return 0, errFractionalCents
		}
		fraction = fraction[:2]
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	cents, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return 0, invalid
	}
	if negative {
		cents.Neg(cents)
	}
	if !cents.IsInt64() || cents.Int64() == math.MinInt64 {
		return 0, invalid
	}
	return cents.Int64(), nil
}
