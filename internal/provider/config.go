package provider

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

// ReadLimits bounds public read work by route class and source.
type ReadLimits struct {
	// DirectPerMinute is the aggregate direct-read token capacity.
	DirectPerMinute int
	// DirectPerSourcePerMinute is the per-source direct-read capacity.
	DirectPerSourcePerMinute int
	// ListingPerMinute is the aggregate listing token capacity.
	ListingPerMinute int
	// ListingPerSourcePerMinute is the per-source listing capacity.
	ListingPerSourcePerMinute int
	// MaxConcurrent is the disclosed concurrency limit; the core does not enforce it.
	MaxConcurrent int
	// MaxSources bounds retained source buckets.
	MaxSources int
	// SettlementPerMinute is the aggregate capacity for settlement reads.
	SettlementPerMinute int
	// SettlementPerSourcePerMinute is the per-source capacity for settlement reads.
	SettlementPerSourcePerMinute int
}

// RetentionConfig configures the initial signed retention commitment.
type RetentionConfig struct {
	// Seconds is the declared minimum retention period.
	Seconds int64
	// Payout names settlement payout recipients.
	Payout wire.PayoutRecipients
}

// OperatorConfig supplies operator disclosure fields for later provider routes.
type OperatorConfig struct {
	// LegalContact is the disclosed legal contact.
	LegalContact, AbuseContact, PrivacyContact string
	// MarketsAssessed lists assessed operating markets.
	MarketsAssessed []string
}

// Config contains the provider identity, trust root, stores, and runtime limits.
// Key rotation is not supported in this release; settlement claims always use a null succession_id.
type Config struct {
	// DataDir is the role data directory; empty selects in-memory stores.
	DataDir string
	// ControlCheckpointDir holds the signed control and intake checkpoints and their first-start markers. It must lie
	// outside DataDir and is required when DataDir is set. Dev mode defaults it to a sibling of DataDir named after it
	// with a "-checkpoint" suffix.
	ControlCheckpointDir string
	// Key is the provider's single signing key for this release.
	Key keys.PrivateKey
	// Genesis is the release trust root.
	Genesis wire.GenesisManifest
	// Trust verifies adopted governance objects.
	Trust *governance.Trust
	// Policies is the initial verified distribution-policy history.
	Policies []wire.DistributionPolicy
	// FeeSchedules is the initial release-approved schedule set.
	FeeSchedules []wire.ApprovedFeeSchedule
	// Faux is the chain interface used for provider payments.
	Faux faux.Chain
	// Now supplies the provider clock.
	Now func() time.Time
	// Random supplies nonces and identifiers.
	Random io.Reader
	// PublicOrigin is the HTTPS origin named in signed documents.
	PublicOrigin string
	// Dev enables local development behavior.
	Dev bool
	// Reads configures anonymous capacity.
	Reads ReadLimits
	// Snapshots configures bounded discovery snapshots.
	Snapshots discovery.SnapshotLimits
	// Retention configures the initial signed declaration.
	Retention RetentionConfig
	// Operator contains public operator disclosure fields.
	Operator OperatorConfig
	// AdmissionStorageBudget bounds retained message bytes plus each new admission; zero selects one GiB.
	AdmissionStorageBudget int64
	// DirectCacheMaxAge bounds direct-read caching; zero selects the one-hour default.
	DirectCacheMaxAge time.Duration
	// QuoteLifetime bounds fee quote lifetime.
	QuoteLifetime time.Duration
	// ClaimPeriod bounds settlement claim periods.
	ClaimPeriod time.Duration
	// WithdrawalLabel identifies provider withdrawal destinations.
	WithdrawalLabel string
	// RecipientLabel identifies provider faux deposit destinations.
	RecipientLabel string
	// ContentStore overrides the content store, primarily for failure injection.
	ContentStore store.Store
	// ControlStore overrides the independent control store.
	ControlStore store.Store
}

var errInvalidConfig = errors.New("provider: invalid configuration")

func (c *Config) validate() error {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Random == nil {
		c.Random = cryptorand.Reader
	}
	if c.AdmissionStorageBudget < 0 {
		return invalidField("AdmissionStorageBudget", "must not be negative")
	}
	if c.AdmissionStorageBudget == 0 {
		c.AdmissionStorageBudget = 1 << 30
	}
	if c.DirectCacheMaxAge == 0 {
		c.DirectCacheMaxAge = time.Hour
	}
	if c.DirectCacheMaxAge < 0 || c.DirectCacheMaxAge > time.Hour {
		return invalidField("DirectCacheMaxAge", "must be between zero and one hour")
	}
	if c.DirectCacheMaxAge%time.Second != 0 {
		return invalidField("DirectCacheMaxAge", "must be whole seconds")
	}
	if c.QuoteLifetime == 0 {
		c.QuoteLifetime = 600 * time.Second
	}
	if c.QuoteLifetime <= 0 || c.QuoteLifetime > time.Hour {
		return invalidField("QuoteLifetime", "must be between 1 second and one hour")
	}
	if c.ClaimPeriod == 0 {
		c.ClaimPeriod = 24 * time.Hour
	}
	if c.ClaimPeriod <= 0 || c.ClaimPeriod > 24*time.Hour {
		return invalidField("ClaimPeriod", "must be between 1 second and 24 hours")
	}
	if c.Retention.Seconds == 0 {
		c.Retention.Seconds = 31536000
	}
	if c.Retention.Seconds < 31536000 {
		return invalidField("Retention.Seconds", "must be at least 31536000")
	}
	if len(c.Key) != 64 {
		return invalidField("Key", "must be a 64-byte private key")
	}
	origin, err := url.Parse(c.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" || !strings.HasPrefix(c.PublicOrigin, "https://") {
		return invalidField("PublicOrigin", "must be an HTTPS origin")
	}
	if c.Faux == nil {
		return invalidField("Faux", "is required")
	}
	if c.Trust == nil {
		return invalidField("Trust", "is required")
	}
	if err := c.Reads.validate(); err != nil {
		return err
	}
	if err := c.Snapshots.Validate(); err != nil {
		return fmt.Errorf("%w: Snapshots: %v", errInvalidConfig, err)
	}
	return nil
}

func invalidField(field, reason string) error {
	return fmt.Errorf("%w: %s %s", errInvalidConfig, field, reason)
}

// DevConfig returns the deterministic local provider configuration.
func DevConfig(root string) (Config, error) {
	now := time.Now
	trust, err := devnet.Trust(now())
	if err != nil {
		return Config{}, err
	}
	return Config{DataDir: store.DataDir(root, "provider"), Key: devnet.ProviderKey(0), Genesis: devnet.Genesis(), Trust: trust, Policies: devnet.Policies(), FeeSchedules: devnet.FeeSchedules(), Faux: faux.NewClient("http://127.0.0.1:8448", nil), Now: now, PublicOrigin: "https://127.0.0.1:8445", Dev: true, ClaimPeriod: time.Minute, RecipientLabel: keys.DIDOf(devnet.ProviderKey(0)), WithdrawalLabel: "provider/0/withdrawal", Operator: OperatorConfig{LegalContact: "legal@provider.invalid", AbuseContact: "abuse@provider.invalid", PrivacyContact: "privacy@provider.invalid"}}, nil
}

// TestConfig returns deterministic dev identities and in-memory storage for tests.
func TestConfig(now func() time.Time) (Config, error) {
	if now == nil {
		return Config{}, errInvalidConfig
	}
	trust, err := devnet.Trust(now())
	if err != nil {
		return Config{}, err
	}
	chain, err := faux.Open(faux.Config{Now: now})
	if err != nil {
		return Config{}, err
	}
	return Config{Key: devnet.ProviderKey(0), Genesis: devnet.Genesis(), Trust: trust, Policies: devnet.Policies(), FeeSchedules: devnet.FeeSchedules(), Faux: chain, Now: now, Random: newDeterministicReader(), PublicOrigin: "https://127.0.0.1:8445", Dev: true, ClaimPeriod: time.Minute, RecipientLabel: keys.DIDOf(devnet.ProviderKey(0)), WithdrawalLabel: "provider/0/withdrawal"}, nil
}

type deterministicReader struct {
	mu sync.Mutex
	r  *randv2.ChaCha8
}

func newDeterministicReader() io.Reader {
	var seed [32]byte
	copy(seed[:], []byte("ForYou provider deterministic test randomness"))
	return &deterministicReader{r: randv2.NewChaCha8(seed)}
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Read(p)
}
