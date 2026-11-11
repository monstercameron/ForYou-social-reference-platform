package replica

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const manifestStore = "replica.manifest"

type manifestSaved struct {
	Version       int64  `json:"version"`
	ContentDigest string `json:"content_digest"`
	ExpiresAt     string `json:"expires_at"`
	Body          []byte `json:"body"`
}

type manifestPolicySource struct {
	Origin     string `json:"origin"`
	ProviderID string `json:"provider_id"`
}

type manifestPolicyLimits struct {
	DirectPerMinute           int   `json:"direct_per_minute"`
	DirectPerSourcePerMinute  int   `json:"direct_per_source_per_minute"`
	ListingPerMinute          int   `json:"listing_per_minute"`
	ListingPerSourcePerMinute int   `json:"listing_per_source_per_minute"`
	MaxSources                int   `json:"max_sources"`
	MaxMessageBytes           int   `json:"max_message_bytes"`
	MaxPageBytes              int   `json:"max_page_bytes"`
	MaxSnapshots              int   `json:"max_snapshots"`
	MaxSnapshotBytes          int64 `json:"max_snapshot_bytes"`
	SnapshotLifetimeSeconds   int64 `json:"snapshot_lifetime_seconds"`
	SnapshotScanBudget        int   `json:"snapshot_scan_budget"`
}

type manifestPolicyDocument struct {
	Mode    string                 `json:"mode"`
	Purpose string                 `json:"purpose"`
	Sources []manifestPolicySource `json:"pinned_sources"`
	Limits  manifestPolicyLimits   `json:"limits"`
	Rights  manifestPolicyRights   `json:"rights"`
	Notice  string                 `json:"notice"`
}

type manifestPolicyRights struct {
	Free    bool   `json:"free"`
	Channel string `json:"channel"`
}

func init() {
	routeGroups = append(routeGroups, manifestRegisterRoutes)
}

func manifestRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/foryou-operator.json", s.readManifest)
	mux.HandleFunc("/policy", s.readPolicy)
}

func (s *Service) readPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		readMethodNotAllowed(w, "GET")
		return
	}
	if s.PublicOrigin() == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	body, err := s.manifestPolicy()
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func (s *Service) readManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		readMethodNotAllowed(w, "GET")
		return
	}
	if s.PublicOrigin() == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	policy, err := s.manifestPolicy()
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	policyDigestBytes := sha256.Sum256(policy)
	policyDigest := hex.EncodeToString(policyDigestBytes[:])
	operator := s.Operator()
	markets := append([]string{}, operator.MarketsAssessed...)
	m := wire.OperatorManifest{
		OperatorID: s.ReplicaID(), ServiceRoles: []string{"storage", "index"},
		ServiceEndpoints: []string{s.PublicOrigin()}, SupportedProfiles: []string{"2.0"},
		PolicyURL: s.PublicOrigin() + "/policy", PolicyDigest: policyDigest,
		LegalContact: operator.LegalContact, AbuseContact: operator.AbuseContact,
		PrivacyContact: operator.PrivacyContact, AppealURL: s.PublicOrigin() + "/policy#rights",
		MarketsAssessed: markets,
	}
	contentBytes, err := canon.EncodeWithout(m, "manifest_version", "issued_at", "expires_at", "signature")
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	contentHash := sha256.Sum256(contentBytes)
	now := s.Now()
	var body []byte
	err = s.Store().Update(func(tx store.Tx) error {
		saved, exists, err := (store.Collection[manifestSaved]{Name: manifestStore}).Get(tx, "current")
		if err != nil {
			return err
		}
		renew := !exists || saved.ContentDigest != hex.EncodeToString(contentHash[:])
		if exists && !renew {
			expires, parseErr := wire.ParseTime(saved.ExpiresAt)
			if parseErr != nil || expires.Sub(now) < 7*24*time.Hour {
				renew = true
			}
		}
		if !renew {
			body = append([]byte(nil), saved.Body...)
			return nil
		}
		version := int64(1)
		if exists {
			version = saved.Version + 1
		}
		m.ManifestVersion = version
		m.IssuedAt = now.Format("2006-01-02T15:04:05Z")
		m.ExpiresAt = now.Add(30 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
		encodedUnsigned, err := canon.EncodeWithout(m, "signature")
		if err != nil {
			return err
		}
		unsignedValue, err := canon.Parse(encodedUnsigned, canon.Limits{MaxDepth: 32})
		if err != nil {
			return err
		}
		signature, err := s.Sign("operator-manifest", unsignedValue)
		if err != nil {
			return err
		}
		m.Signature = signature
		body, err = canon.Encode(m)
		if err != nil {
			return err
		}
		value, err := canon.Parse(body, canon.Limits{MaxDepth: 32})
		if err != nil {
			return err
		}
		if err := wire.Check("OperatorManifest", value); err != nil {
			return err
		}
		return (store.Collection[manifestSaved]{Name: manifestStore}).Put(tx, "current", manifestSaved{
			Version: version, ContentDigest: hex.EncodeToString(contentHash[:]), ExpiresAt: m.ExpiresAt, Body: body,
		})
	})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func (s *Service) manifestPolicy() ([]byte, error) {
	limits := s.Config().Reads
	snapshot := s.Snapshots().Limits()
	sources := s.Sources()
	pinned := make([]manifestPolicySource, 0, len(sources))
	for _, source := range sources {
		pinned = append(pinned, manifestPolicySource{Origin: source.Origin, ProviderID: source.ProviderID})
	}
	return canon.Encode(manifestPolicyDocument{
		Mode:    "independently-keyed development mirror",
		Purpose: "Copies independently pinned provider records for public reading. Acknowledgments by this replica are evidence about this mirror's copy and are not acceptance by the admitting provider.",
		Sources: pinned,
		Limits: manifestPolicyLimits{
			DirectPerMinute: limits.DirectPerMinute, DirectPerSourcePerMinute: limits.DirectPerSourcePerMinute,
			ListingPerMinute: limits.ListingPerMinute, ListingPerSourcePerMinute: limits.ListingPerSourcePerMinute,
			MaxSources: limits.MaxSources, MaxMessageBytes: canon.MessageLimits.MaxBytes, MaxPageBytes: discovery.MaxPageBytes,
			MaxSnapshots: snapshot.MaxSnapshots, MaxSnapshotBytes: snapshot.MaxBytes,
			SnapshotLifetimeSeconds: int64(snapshot.Lifetime.Seconds()), SnapshotScanBudget: snapshot.ScanBudget,
		},
		Rights: manifestPolicyRights{
			Free:    true,
			Channel: "Send rights requests, claims, and appeals to the legal contact " + s.Operator().LegalContact + ", abuse contact " + s.Operator().AbuseContact + ", or privacy contact " + s.Operator().PrivacyContact + ".",
		},
		Notice: "This mirror declares no provider retention duty and does not replace or extend any admitting provider's obligations.",
	})
}
