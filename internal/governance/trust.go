package governance

import (
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Trust is a verified release timeline and Foundation certificate, closure and head set.
type Trust struct {
	timeline     *Timeline
	certificates []wire.FoundationKeyCertificate
	closures     map[string]wire.FoundationKeyClosure
	heads        []wire.FoundationLogHead
}

// NewTrust validates the genesis trust root and all supplied governance records.
func NewTrust(g wire.GenesisManifest, updates []wire.ReleaseKeySetUpdate, certs []wire.FoundationKeyCertificate, closures []wire.FoundationKeyClosure, heads []wire.FoundationLogHead) (*Trust, error) {
	genesisSet, err := CheckGenesis(g)
	if err != nil {
		return nil, err
	}
	timeline, err := NewTimeline(genesisSet, updates)
	if err != nil {
		return nil, err
	}
	trust := &Trust{timeline: timeline, closures: make(map[string]wire.FoundationKeyClosure)}
	certificateBytes := make(map[string]string)
	addCertificate := func(c wire.FoundationKeyCertificate, genesis bool) error {
		encoded, err := canon.Encode(c)
		if err != nil {
			return fmt.Errorf("%w: certificate encoding: %v", ErrInvalid, err)
		}
		if previous, exists := certificateBytes[c.CertificateID]; exists {
			if previous == string(encoded) {
				return nil
			}
			return fmt.Errorf("%w: conflicting certificate ID", ErrInvalid)
		}
		issuedAt, err := wire.ParseTime(c.IssuedAt)
		if err != nil {
			return fmt.Errorf("%w: certificate issued_at", ErrInvalid)
		}
		set, err := timeline.SetAt(issuedAt, c)
		if err != nil {
			return err
		}
		if err := CheckCertificate(c, set, genesis); err != nil {
			return err
		}
		certificateBytes[c.CertificateID] = string(encoded)
		trust.certificates = append(trust.certificates, c)
		return nil
	}
	for _, c := range g.FoundationKeyCertificates {
		if err := addCertificate(c, true); err != nil {
			return nil, err
		}
	}
	for _, c := range certs {
		if err := addCertificate(c, false); err != nil {
			return nil, err
		}
	}
	for _, head := range heads {
		if err := VerifyLogHead(head); err != nil {
			return nil, err
		}
		trust.heads = append(trust.heads, head)
	}
	for _, closure := range closures {
		if _, exists := trust.closures[closure.FoundationID]; exists {
			return nil, fmt.Errorf("%w: duplicate Foundation closure", ErrInvalid)
		}
		closedAt, err := wire.ParseTime(closure.ClosedAt)
		if err != nil {
			return nil, fmt.Errorf("%w: closure closed_at", ErrInvalid)
		}
		set, err := timeline.SetAt(closedAt, closure)
		if err != nil {
			return nil, err
		}
		if err := CheckClosure(closure, set); err != nil {
			return nil, err
		}
		trust.closures[closure.FoundationID] = closure
	}
	sort.Slice(trust.certificates, func(i, j int) bool {
		if trust.certificates[i].FoundationID == trust.certificates[j].FoundationID {
			return trust.certificates[i].ValidFrom < trust.certificates[j].ValidFrom
		}
		return trust.certificates[i].FoundationID < trust.certificates[j].FoundationID
	})
	return trust, nil
}

// VerifyRelease verifies certificates, closures, fee approvals, and policies at signedAt, including carry-over.
func (t *Trust) VerifyRelease(label string, unsigned any, sigs []wire.ReleaseSignature, signedAt time.Time, signed any) error {
	if t == nil || !wholeSecond(signedAt) {
		return fmt.Errorf("%w: release signed time", ErrInvalid)
	}
	definition, ok := releaseSchema(label)
	if !ok || label == "release-key-set-update" || label == "genesis-manifest" {
		return fmt.Errorf("%w: unsupported release label", ErrInvalid)
	}
	if err := schemaCheck(definition, signed); err != nil {
		return err
	}
	unsignedBytes, err := canon.Encode(unsigned)
	if err != nil {
		return fmt.Errorf("%w: release signing object: %v", ErrInvalid, err)
	}
	signedBytes, err := canon.EncodeWithout(signed, "release_signatures")
	if err != nil || string(unsignedBytes) != string(signedBytes) {
		return fmt.Errorf("%w: release object does not match signing object", ErrInvalid)
	}
	value, err := canon.Encode(signed)
	if err != nil {
		return fmt.Errorf("%w: release object encoding: %v", ErrInvalid, err)
	}
	parsed, err := canon.Parse(value, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: release object encoding: %v", ErrInvalid, err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: release object must be an object", ErrInvalid)
	}
	if err := checkSignedTime(object, releaseTimeField(label), signedAt); err != nil {
		return err
	}
	actualSignatures, err := canon.Encode(object["release_signatures"])
	providedSignatures, providedErr := canon.Encode(sigs)
	if err != nil || providedErr != nil || string(actualSignatures) != string(providedSignatures) {
		return fmt.Errorf("%w: release signatures do not match signed object", ErrInvalid)
	}
	set, err := t.timeline.SetAt(signedAt, signed)
	if err != nil {
		return err
	}
	return VerifyThreshold(label, unsigned, sigs, set)
}

// RecipientCounts reports whether a transfer paid a certified, open key's recipient at blockTime.
func (t *Trust) RecipientCounts(network, address string, blockTime time.Time) bool {
	if t == nil || !wholeSecond(blockTime) {
		return false
	}
	for _, certificate := range t.certificates {
		from, err := wire.ParseTime(certificate.ValidFrom)
		if err != nil || blockTime.Before(from) {
			continue
		}
		var recipient string
		switch network {
		case wire.BitcoinMainnet:
			recipient = certificate.Recipients.BitcoinMainnet
		case wire.EthereumMainnet:
			recipient = certificate.Recipients.EthereumMainnet
		default:
			continue
		}
		if recipient != address {
			continue
		}
		closure, closed := t.closures[certificate.FoundationID]
		if !closed {
			return true
		}
		closedAt, err := wire.ParseTime(closure.ClosedAt)
		if err == nil && blockTime.Before(closedAt) {
			return true
		}
	}
	return false
}

// VerifyFoundation verifies a Foundation signature at signedAt and applies closure evidence rules.
func (t *Trust) VerifyFoundation(obj any, signedAt time.Time, label string, proof *wire.FoundationLogProof) error {
	if t == nil || !wholeSecond(signedAt) || !foundationLabel(label) {
		return fmt.Errorf("%w: Foundation signed time or label", ErrInvalid)
	}
	value, err := canon.Encode(obj)
	if err != nil {
		return fmt.Errorf("%w: Foundation object encoding: %v", ErrInvalid, err)
	}
	parsed, err := canon.Parse(value, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: Foundation object encoding: %v", ErrInvalid, err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: Foundation object must be an object", ErrInvalid)
	}
	definition := ""
	switch label {
	case "vault-receipt":
		definition = "VaultReceipt"
	case "distribution-statement":
		definition = "DistributionStatement"
	case "vault-status":
		definition = "VaultStatus"
	case "foundation-log-head":
		definition = "FoundationLogHead"
	}
	if err := wire.Check(definition, parsed); err != nil {
		return fmt.Errorf("%w: Foundation object shape: %v", ErrInvalid, err)
	}
	if err := checkSignedTime(object, foundationTimeField(label), signedAt); err != nil {
		return err
	}
	foundationID, ok := object["foundation_id"].(string)
	if !ok {
		return fmt.Errorf("%w: missing foundation_id", ErrInvalid)
	}
	validCertificate := false
	for _, certificate := range t.certificates {
		from, fromErr := wire.ParseTime(certificate.ValidFrom)
		until, untilErr := wire.ParseTime(certificate.ValidUntil)
		if certificate.FoundationID == foundationID && fromErr == nil && untilErr == nil && !signedAt.Before(from) && signedAt.Before(until) {
			validCertificate = true
			break
		}
	}
	if !validCertificate {
		return fmt.Errorf("%w: Foundation key not certified at signed time", ErrInvalid)
	}
	signature, ok := object["foundation_signature"].(string)
	if !ok {
		return fmt.Errorf("%w: missing foundation_signature", ErrInvalid)
	}
	unsigned, err := canon.EncodeWithout(obj, "foundation_signature")
	if err != nil {
		return fmt.Errorf("%w: Foundation signing object: %v", ErrInvalid, err)
	}
	if err := keys.Verify(foundationID, label, unsigned, signature); err != nil {
		return fmt.Errorf("%w: Foundation signature: %v", ErrInvalid, err)
	}
	closure, closed := t.closures[foundationID]
	if !closed {
		return nil
	}
	if proof == nil || proof.Size != closure.Head.Size || closure.Head.Size <= 0 {
		return fmt.Errorf("%w: closed Foundation key requires closure-head proof", ErrInvalid)
	}
	if err := schemaCheck("FoundationLogProof", *proof); err != nil {
		return err
	}
	hash, err := ObjectHash(obj)
	if err != nil {
		return err
	}
	root, err := RootFromProof(hash, *proof)
	if err != nil || root != closure.Head.Root {
		return fmt.Errorf("%w: Foundation object not in closure log", ErrInvalid)
	}
	for _, voided := range closure.Voided {
		if hash == voided {
			return fmt.Errorf("%w: Foundation object voided by closure", ErrInvalid)
		}
	}
	return nil
}

// CurrentFoundationID returns an open Foundation key with a certificate valid at at.
func (t *Trust) CurrentFoundationID(at time.Time) (string, bool) {
	if t == nil || !wholeSecond(at) {
		return "", false
	}
	for _, certificate := range t.certificates {
		from, fromErr := wire.ParseTime(certificate.ValidFrom)
		until, untilErr := wire.ParseTime(certificate.ValidUntil)
		if fromErr != nil || untilErr != nil || at.Before(from) || !at.Before(until) {
			continue
		}
		if _, closed := t.closures[certificate.FoundationID]; !closed {
			return certificate.FoundationID, true
		}
	}
	return "", false
}

// ClosureEvidence returns verified heads larger than the authoritative closure head.
func (t *Trust) ClosureEvidence() []wire.FoundationLogHead {
	if t == nil {
		return nil
	}
	var evidence []wire.FoundationLogHead
	for _, head := range t.heads {
		closure, ok := t.closures[head.FoundationID]
		if ok && head.Size > closure.Head.Size {
			evidence = append(evidence, head)
		}
	}
	return evidence
}

func wholeSecond(at time.Time) bool { return !at.IsZero() && at.Nanosecond() == 0 }

func foundationLabel(label string) bool {
	switch label {
	case "vault-receipt", "distribution-statement", "vault-status", "foundation-log-head":
		return true
	default:
		return false
	}
}

func releaseTimeField(label string) string {
	switch label {
	case "release-key-set-update":
		return "effective_at"
	case "foundation-key-certificate":
		return "issued_at"
	case "foundation-key-closure":
		return "closed_at"
	case "genesis-manifest":
		return "signed_at"
	case "fee-schedule-approval", "distribution-policy":
		return "approved_at"
	default:
		return ""
	}
}

func foundationTimeField(label string) string {
	switch label {
	case "vault-receipt":
		return "stored_at"
	case "distribution-statement", "vault-status":
		return "period_end"
	case "foundation-log-head":
		return "signed_at"
	default:
		return ""
	}
}

func checkSignedTime(object map[string]any, field string, signedAt time.Time) error {
	value, ok := object[field].(string)
	if !ok {
		return fmt.Errorf("%w: missing signed time %s", ErrInvalid, field)
	}
	parsed, err := wire.ParseTime(value)
	if err != nil || !parsed.Equal(signedAt) {
		return fmt.Errorf("%w: signed time mismatch for %s", ErrInvalid, field)
	}
	return nil
}
