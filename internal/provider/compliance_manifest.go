package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const manifestStore = "provider.compliance.manifest"

// manifestSupportedProfile names this implementation's wire profile string; the specification does not prescribe the representation, and messages use major 2, minor 0.
const manifestSupportedProfile = "2.0"

type manifestSaved struct {
	Version       int64  `json:"version"`
	ContentDigest string `json:"content_digest"`
	ExpiresAt     string `json:"expires_at"`
	Body          []byte `json:"body"`
}

type manifestReadLimits struct {
	DirectPerMinute           int  `json:"direct_per_minute"`
	DirectPerSourcePerMinute  int  `json:"direct_per_source_per_minute"`
	ListingPerMinute          int  `json:"listing_per_minute"`
	ListingPerSourcePerMinute int  `json:"listing_per_source_per_minute"`
	MaxConcurrent             int  `json:"max_concurrent"`
	MaxSources                int  `json:"max_sources"`
	MaxConcurrentEnforced     bool `json:"max_concurrent_enforced"`
	ReadOtherUsesListing      bool `json:"read_other_uses_listing_buckets"`
}

type manifestSnapshotLimits struct {
	MaxSnapshots int   `json:"max_snapshots"`
	MaxBytes     int64 `json:"max_bytes"`
	Lifetime     int64 `json:"lifetime_seconds"`
	ScanBudget   int   `json:"scan_budget"`
}

type manifestServiceLimits struct {
	MaxOutstanding    int   `json:"max_outstanding"`
	MaxPerSource      int   `json:"max_per_source"`
	ReservedProtected int   `json:"reserved_protected"`
	ChallengeLifetime int64 `json:"challenge_lifetime_seconds"`
	MaxRequestBytes   int   `json:"max_request_bytes"`
	MaxResourceBytes  int   `json:"max_resource_bytes"`
}

type manifestResponseSizeLimits struct {
	DirectGETBytes          int `json:"direct_get_bytes"`
	ListingChangesPageBytes int `json:"listing_and_changes_page_bytes"`
}

type manifestRetention struct {
	URL           string `json:"url"`
	PeriodSeconds *int64 `json:"period_seconds,omitempty"`
}

type manifestPeering struct {
	Policy             string `json:"policy"`
	DefaultAllowlist   bool   `json:"default_allowlist"`
	TrustRelationships string `json:"trust_relationships"`
}

type manifestRights struct {
	Free                        bool     `json:"free"`
	Channel                     string   `json:"channel"`
	NoFeeMembershipBalanceOrTx  bool     `json:"no_fee_membership_balance_or_transaction"`
	NoRootKeyOrAccountRequired  bool     `json:"no_root_key_or_account_required"`
	ThirdPartyPayersMayUse      bool     `json:"third_party_payers_may_use"`
	DescribedPeopleMayUse       bool     `json:"people_described_in_another_authors_record_may_use"`
	OptionalHTTPIntakeAvailable bool     `json:"optional_http_intake_available"`
	IntakeMaxRequestBytes       int      `json:"intake_max_request_bytes"`
	IntakeRecoveryRetention     string   `json:"intake_recovery_retention"`
	AlternativeContacts         []string `json:"alternative_contacts"`
	Claims                      string   `json:"claims"`
}

type manifestPreparation struct {
	Deadline string `json:"deadline"`
	Refund   string `json:"refund"`
}

type manifestDeposits struct {
	OpenInvoiceLimit             string `json:"open_invoice_limit"`
	ProviderWideOpenInvoiceLimit string `json:"provider_wide_open_invoice_limit"`
	InvoiceExpiry                string `json:"invoice_expiry"`
}

type manifestRevocations struct {
	Capacity string `json:"capacity"`
}

type manifestCrediting struct {
	BitcoinConfirmations int `json:"bitcoin_mainnet_confirmations"`
	EthereumFinality     int `json:"ethereum_mainnet_finality_depth"`
}

type manifestCustody struct {
	HoldsBalance      bool              `json:"holds_balance"`
	SolvencyGuarantee bool              `json:"solvency_guarantee"`
	Scope             string            `json:"scope"`
	Fee               string            `json:"fee"`
	Settlement        string            `json:"settlement"`
	CreditingRule     string            `json:"crediting_rule"`
	Crediting         manifestCrediting `json:"crediting"`
	OutsideFeeCosts   string            `json:"outside_fee_costs"`
	Loss              string            `json:"loss"`
	Remedy            string            `json:"remedy"`
	Linkability       string            `json:"linkability"`
	CustodyCapPosts   int               `json:"custody_cap_posts"`
}

type manifestFeeSchedule struct {
	Schedule   wire.FeeSchedule `json:"schedule"`
	CustodyCap string           `json:"custody_cap_base_units"`
}

type manifestPolicyDocument struct {
	DevMode             bool                       `json:"dev_mode"`
	Deployment          string                     `json:"deployment"`
	LegalCompliance     string                     `json:"legal_compliance"`
	ManifestNotice      string                     `json:"manifest_notice"`
	SignedHTTPSOrigin   string                     `json:"signed_https_origin"`
	Transport           string                     `json:"transport"`
	ReadLimits          manifestReadLimits         `json:"read_limits"`
	ResponseSizeLimits  manifestResponseSizeLimits `json:"response_size_limits"`
	SnapshotLimits      manifestSnapshotLimits     `json:"snapshot_limits"`
	Retention           manifestRetention          `json:"retention"`
	RestrictionStatuses []Restriction              `json:"restriction_statuses"`
	Peering             manifestPeering            `json:"peering"`
	Rights              manifestRights             `json:"rights"`
	Preparation         manifestPreparation        `json:"preparation"`
	Deposits            manifestDeposits           `json:"deposits"`
	ServiceChallenges   manifestServiceLimits      `json:"service_challenges"`
	Revocations         manifestRevocations        `json:"revocations"`
	Custody             manifestCustody            `json:"custody"`
	FeeSchedules        []manifestFeeSchedule      `json:"fee_schedules"`
}

func init() {
	routeGroups = append(routeGroups, manifestRegisterRoutes)
}

func manifestRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/foryou-operator.json", s.manifestRead)
	mux.HandleFunc("GET /policy", s.manifestPolicyRead)
	// TestConfig and other route groups' tests run without contacts, so the manifest is unavailable rather than failing the service.
	if manifestContactsConfigured(s) {
		if err := s.manifestRenew(); err != nil {
			log.Printf("provider operator manifest: %v", err)
		}
	}
	s.Every("operator-manifest", time.Hour, func(_ context.Context, _ time.Time) error {
		if !manifestContactsConfigured(s) {
			return nil
		}
		return s.manifestRenew()
	})
}

func manifestContactsConfigured(s *Service) bool {
	operator := s.Config().Operator
	return operator.LegalContact != "" && operator.AbuseContact != "" && operator.PrivacyContact != ""
}

func (s *Service) manifestPolicyRead(w http.ResponseWriter, r *http.Request) {
	if !s.LimitRead(w, r, ReadOther) {
		return
	}
	body, err := s.manifestPolicy()
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func (s *Service) manifestRead(w http.ResponseWriter, r *http.Request) {
	if !s.LimitRead(w, r, ReadOther) {
		return
	}
	if err := s.manifestRenew(); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	var body []byte
	err := s.Store().View(func(tx store.Tx) error {
		saved, ok, err := (store.Collection[manifestSaved]{Name: manifestStore}).Get(tx, "current")
		if err != nil {
			return err
		}
		if !ok || len(saved.Body) == 0 {
			return errors.New("provider: operator manifest unavailable")
		}
		body = append([]byte(nil), saved.Body...)
		return nil
	})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func (s *Service) manifestRenew() error {
	origin := strings.TrimSuffix(s.cfg.PublicOrigin, "/")
	policy, err := s.manifestPolicy()
	if err != nil {
		return err
	}
	policyHash := sha256.Sum256(policy)
	operator := s.Config().Operator
	markets := append([]string{}, operator.MarketsAssessed...)
	m := wire.OperatorManifest{
		OperatorID: s.ProviderID(), ServiceRoles: []string{"ingress", "storage", "index", "payments"},
		ServiceEndpoints: []string{origin}, SupportedProfiles: []string{manifestSupportedProfile},
		PolicyURL: origin + "/policy", PolicyDigest: hex.EncodeToString(policyHash[:]),
		LegalContact: operator.LegalContact, AbuseContact: operator.AbuseContact,
		PrivacyContact: operator.PrivacyContact, AppealURL: origin + "/policy#rights",
		MarketsAssessed: markets,
	}
	content, err := canon.EncodeWithout(m, "manifest_version", "issued_at", "expires_at", "signature")
	if err != nil {
		return err
	}
	contentHash := sha256.Sum256(content)
	contentDigest := hex.EncodeToString(contentHash[:])
	now := s.Now()
	return s.Store().Update(func(tx store.Tx) error {
		collection := store.Collection[manifestSaved]{Name: manifestStore}
		saved, exists, err := collection.Get(tx, "current")
		if err != nil {
			return err
		}
		renew := !exists || saved.ContentDigest != contentDigest
		if exists && !renew {
			expires, parseErr := wire.ParseTime(saved.ExpiresAt)
			if parseErr != nil || expires.Sub(now) < 7*24*time.Hour {
				renew = true
			}
		}
		if !renew {
			return nil
		}
		if err := s.CheckClock(); err != nil {
			return err
		}
		version := int64(1)
		if exists {
			version = saved.Version + 1
		}
		m.ManifestVersion = version
		m.IssuedAt = now.Format(time.RFC3339)
		m.ExpiresAt = now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
		unsigned, err := canon.EncodeWithout(m, "signature")
		if err != nil {
			return err
		}
		value, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
		if err != nil {
			return err
		}
		m.Signature, err = s.Sign("operator-manifest", value)
		if err != nil {
			return err
		}
		body, err := canon.Encode(m)
		if err != nil {
			return err
		}
		value, err = canon.Parse(body, canon.Limits{MaxDepth: 32})
		if err != nil {
			return err
		}
		if err := wire.Check("OperatorManifest", value); err != nil {
			return err
		}
		return collection.Put(tx, "current", manifestSaved{
			Version: version, ContentDigest: contentDigest, ExpiresAt: m.ExpiresAt, Body: body,
		})
	})
}

func (s *Service) manifestPolicy() ([]byte, error) {
	cfg := s.Config()
	origin := strings.TrimSuffix(cfg.PublicOrigin, "/")
	snapshot := s.Snapshots().Limits()
	challenge := s.ServiceAuth.Limits()
	limits := cfg.Reads
	retention := manifestRetention{URL: origin + "/v1/retention"}
	if declaration, ok := s.CurrentDeclaration(); ok {
		seconds := declaration.RetentionSeconds
		retention.PeriodSeconds = &seconds
	}
	schedules := s.FeeSchedules()
	fees := make([]manifestFeeSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		limit, err := fee.CustodyCap(schedule)
		if err != nil {
			return nil, err
		}
		fees = append(fees, manifestFeeSchedule{Schedule: schedule, CustodyCap: limit.String()})
	}
	transport := "The signed manifest URLs name PublicOrigin and use HTTPS."
	deployment := "non-development configuration; not a production deployment"
	if cfg.Dev {
		transport = "Dev mode serves plain HTTP on loopback; signed manifest URLs still name PublicOrigin."
		deployment = "development network"
	}
	creditingRule := "A deposit is credited only after the listed network confirmation or finality condition."
	crediting := manifestCrediting{BitcoinConfirmations: faux.BitcoinConfirmations, EthereumFinality: faux.EthereumFinalityDepth}
	if cfg.Dev {
		creditingRule = "Payments use the faux chain, which accepts every transfer, carries no real value and confirms it at once; the listed depths are the faux chain's."
	}
	return canon.Encode(manifestPolicyDocument{
		DevMode: cfg.Dev, Deployment: deployment,
		LegalCompliance:   "This deployment has no legal compliance assessment; development mode is not legal compliance.",
		ManifestNotice:    "This signed manifest is an operator disclosure, not a compliance certificate.",
		SignedHTTPSOrigin: origin, Transport: transport,
		ReadLimits: manifestReadLimits{
			DirectPerMinute: limits.DirectPerMinute, DirectPerSourcePerMinute: limits.DirectPerSourcePerMinute,
			ListingPerMinute: limits.ListingPerMinute, ListingPerSourcePerMinute: limits.ListingPerSourcePerMinute,
			MaxConcurrent: limits.MaxConcurrent, MaxSources: limits.MaxSources,
			MaxConcurrentEnforced: false, ReadOtherUsesListing: true,
		},
		ResponseSizeLimits: manifestResponseSizeLimits{DirectGETBytes: canon.MessageLimits.MaxBytes, ListingChangesPageBytes: discovery.MaxPageBytes},
		SnapshotLimits: manifestSnapshotLimits{
			MaxSnapshots: snapshot.MaxSnapshots, MaxBytes: snapshot.MaxBytes,
			Lifetime: int64(snapshot.Lifetime.Seconds()), ScanBudget: snapshot.ScanBudget,
		},
		Retention:           retention,
		RestrictionStatuses: []Restriction{PolicyRestricted, LegalRestricted, Restricted},
		Peering:             manifestPeering{Policy: "no peering", DefaultAllowlist: false, TrustRelationships: "none"},
		Rights: manifestRights{
			Free:                       true,
			Channel:                    "Submit notices, data-rights requests, appeals and funding claims at no cost through the legal, abuse or privacy contact. Notices, data-rights requests and appeals can also be submitted at no cost through the optional HTTP intake routes; the legal, abuse and privacy contacts remain the alternative that does not depend on those routes. Intake form definitions are fetched from GET /v1/intake-forms/{form_id}/{form_version}.",
			NoFeeMembershipBalanceOrTx: true, NoRootKeyOrAccountRequired: true,
			ThirdPartyPayersMayUse: true, DescribedPeopleMayUse: true, OptionalHTTPIntakeAvailable: true,
			IntakeMaxRequestBytes:   intakeMaxRequestBytes,
			IntakeRecoveryRetention: "Accepted intake requests and their receipts are kept with no automatic deletion for at least 30 days and for as long as the submission remains pending. Losing the recovery secret does not end the rights, which remain available through the alternative contacts.",
			AlternativeContacts:     []string{cfg.Operator.LegalContact, cfg.Operator.AbuseContact, cfg.Operator.PrivacyContact},
			Claims:                  "Underpaid, overpaid, late and duplicate transfers are never forfeited; they enter the free confidential claims process through the rights channel.",
		},
		Preparation: manifestPreparation{
			Deadline: "An unpublished prepared intent is held until the exclusive preparation deadline after the receipt issued_at.",
			Refund:   "There is no automatic refund before debit.",
		},
		Deposits: manifestDeposits{
			OpenInvoiceLimit:             "The protocol's limit of unexpired, unpaid invoices per author and network applies; pending observed transfers count against it.",
			ProviderWideOpenInvoiceLimit: "A provider-wide cap on open invoices also applies.",
			InvoiceExpiry:                "An invoice stops counting as open when its configured expiry is reached.",
		},
		ServiceChallenges: manifestServiceLimits{
			MaxOutstanding: challenge.MaxOutstanding, MaxPerSource: challenge.MaxPerSource,
			ReservedProtected: challenge.ReservedProtected, ChallengeLifetime: int64(delegation.ChallengeLifetime.Seconds()),
			MaxRequestBytes: delegation.MaxChallengeRequestBytes, MaxResourceBytes: delegation.MaxResourceBytes,
		},
		Revocations: manifestRevocations{Capacity: "Revocation capacity per author is bounded. When exhausted, requests are refused with a capacity error; the root can freeze delegated admission or rotate the authorization epoch through POST /v1/authorization-epochs/control as the safe fallback."},
		Custody: manifestCustody{
			HoldsBalance: true, SolvencyGuarantee: false,
			Scope:         "Balances are specific to one author_id, this provider and one network, are not transferable between providers or authors, and are not a protocol token.",
			Fee:           "Each admitted record is charged the release-approved schedule amount; the provider does not keep the fee and cannot change it.",
			Settlement:    "The provider owes and settles the full debited fee to the ForYou Foundation.",
			CreditingRule: creditingRule, Crediting: crediting,
			OutsideFeeCosts: "Network transaction costs of deposits and withdrawals are separate from the protocol fee.",
			Loss:            "Provider insolvency, custody failure or legal restriction can impair recovery of an unspent balance.",
			Remedy:          "Use the free rights and claims channel and remedies described by the protocol economics.",
			Linkability:     "Public-chain deposits can be correlated with the provider and, through provider records, with the author DID.",
			CustodyCapPosts: fee.CustodyCapPosts,
		},
		FeeSchedules: fees,
	})
}
