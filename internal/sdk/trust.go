package sdk

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/wire"
)

// Pin is the locally configured initial release-key set.
type Pin struct {
	ReleaseKeyIDs []string
	Threshold     int
}

// Publications contains decoded Foundation trust publications and their provenance.
type Publications struct {
	Genesis         wire.GenesisManifest
	Certificates    wire.FoundationCertificatesPage
	FeeSchedules    wire.FeeSchedulesPage
	Distribution    wire.DistributionPage
	GenesisRaw      []byte
	CertificatesRaw []byte
	FeeSchedulesRaw []byte
	DistributionRaw []byte
	Endpoint        string
	FetchedAt       time.Time
}

// RejectedObject describes a Foundation publication excluded from the trust root.
type RejectedObject struct {
	Kind   string
	ID     string
	Reason string
}

// CommitteeView describes the current committee and its handover predecessor.
type CommitteeView struct {
	Current   []string
	PolicyID  string
	Previous  []string
	ChangedAt time.Time
}

// FoundationSummary displays one certified Foundation key.
type FoundationSummary struct {
	FoundationID string
	ValidFrom    string
	ValidUntil   string
	Recipients   wire.FoundationRecipients
}

// ScheduleSummary displays an approved fee schedule.
type ScheduleSummary struct {
	ID                 string
	Network            string
	Revision           int64
	Denomination       string
	BaseFeeBaseUnits   string
	PerKiBFeeBaseUnits string
	EffectiveAt        string
	ExpiresAt          string
}

// TrustSummary contains display-only trust-root information.
type TrustSummary struct {
	ReleaseKeyIDs        []string
	Threshold            int
	Foundations          []FoundationSummary
	Closures             []wire.FoundationKeyClosure
	Schedules            []ScheduleSummary
	Rejected             []RejectedObject
	Committee            CommitteeView
	CommitteeEffectiveAt string
	Endpoint             string
	FetchedAt            time.Time
}

// TrustRoot is an immutable set of verified Foundation publications.
type TrustRoot struct {
	pin          Pin
	trust        *governance.Trust
	timeline     *governance.Timeline
	schedules    []wire.FeeSchedule
	policies     []wire.DistributionPolicy
	rejected     []RejectedObject
	certificates []wire.FoundationKeyCertificate
	closures     []wire.FoundationKeyClosure
	endpoint     string
	fetchedAt    time.Time
}

// BuildTrustRoot verifies a pinned genesis and all usable Foundation publications.
func BuildTrustRoot(pin Pin, pub Publications) (*TrustRoot, error) {
	if err := vfCheckPublicationRaw(pub.GenesisRaw, "GenesisManifest", pub.Genesis); err != nil {
		return nil, fmt.Errorf("genesis publication bytes: %w", err)
	}
	if err := vfCheckPublicationRaw(pub.CertificatesRaw, "FoundationCertificatesPage", pub.Certificates); err != nil {
		return nil, fmt.Errorf("certificates publication bytes: %w", err)
	}
	if err := vfCheckPublicationRaw(pub.FeeSchedulesRaw, "FeeSchedulesPage", pub.FeeSchedules); err != nil {
		return nil, fmt.Errorf("fee schedules publication bytes: %w", err)
	}
	if err := vfCheckPublicationRaw(pub.DistributionRaw, "DistributionPage", pub.Distribution); err != nil {
		return nil, fmt.Errorf("distribution publication bytes: %w", err)
	}
	set, err := governance.CheckGenesis(pub.Genesis)
	if err != nil {
		return nil, fmt.Errorf("genesis: %w", err)
	}
	if pin.Threshold != int(pub.Genesis.ReleaseThreshold) || !vfEqualStrings(pin.ReleaseKeyIDs, pub.Genesis.ReleaseKeyIDs) {
		return nil, fmt.Errorf("release key pin mismatch: local (%v, threshold %d), manifest (%v, threshold %d)", pin.ReleaseKeyIDs, pin.Threshold, pub.Genesis.ReleaseKeyIDs, pub.Genesis.ReleaseThreshold)
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	root := &TrustRoot{pin: Pin{ReleaseKeyIDs: append([]string(nil), pin.ReleaseKeyIDs...), Threshold: pin.Threshold}, endpoint: pub.Endpoint, fetchedAt: pub.FetchedAt}
	updates := append([]wire.ReleaseKeySetUpdate(nil), pub.Certificates.KeySetUpdates...)
	root.timeline, err = governance.NewTimeline(set, updates)
	if err != nil {
		return nil, err
	}
	root.certificates = vfCloneCertificates(append(append([]wire.FoundationKeyCertificate(nil), pub.Genesis.FoundationKeyCertificates...), pub.Certificates.Certificates...))
	root.closures = vfCloneClosures(pub.Certificates.Closures)
	heads := make([]wire.FoundationLogHead, 0, len(root.closures))
	for _, closure := range root.closures {
		heads = append(heads, closure.Head)
	}
	root.trust, err = governance.NewTrust(vfCloneGenesis(pub.Genesis), updates, vfCloneCertificates(pub.Certificates.Certificates), vfCloneClosures(root.closures), heads)
	if err != nil {
		return nil, err
	}
	root.vfApproveGenesisSchedules(pub.Genesis)
	feePairs := append([]wire.ApprovedFeeSchedule(nil), pub.FeeSchedules.Schedules...)
	sort.Slice(feePairs, func(i, j int) bool {
		if feePairs[i].Schedule.Network == feePairs[j].Schedule.Network {
			return feePairs[i].Schedule.Revision < feePairs[j].Schedule.Revision
		}
		return feePairs[i].Schedule.Network < feePairs[j].Schedule.Network
	})
	for _, pair := range feePairs {
		root.vfApproveSchedule(pair.Schedule, pair.Approval)
	}
	root.vfPruneUnpairedRevisions()
	root.vfApprovePolicy(pub.Genesis.DistributionPolicy)
	policies := append([]wire.DistributionPolicy(nil), pub.Distribution.Policies...)
	sort.Slice(policies, func(i, j int) bool { return policies[i].EffectiveAt < policies[j].EffectiveAt })
	for _, policy := range policies {
		root.vfApprovePolicy(policy)
	}
	sort.Slice(root.schedules, func(i, j int) bool {
		if root.schedules[i].Network == root.schedules[j].Network {
			return root.schedules[i].Revision < root.schedules[j].Revision
		}
		return root.schedules[i].Network < root.schedules[j].Network
	})
	sort.Slice(root.policies, func(i, j int) bool { return root.policies[i].EffectiveAt < root.policies[j].EffectiveAt })
	return root, nil
}

func vfCheckPublicationRaw(raw []byte, definition string, decoded any) error {
	if len(raw) == 0 {
		return nil
	}
	canonicalRaw, err := canon.Canonicalize(raw, canon.Limits{MaxDepth: 32})
	if err != nil {
		return err
	}
	canonicalDecoded, err := canon.Encode(decoded)
	if err != nil {
		return err
	}
	if string(canonicalRaw) != string(canonicalDecoded) {
		return errors.New("raw publication does not match decoded object")
	}
	var value any
	value, err = canon.Parse(raw, canon.Limits{MaxDepth: 32})
	if err != nil {
		return err
	}
	return wire.Check(definition, value)
}

func vfCloneCertificates(input []wire.FoundationKeyCertificate) []wire.FoundationKeyCertificate {
	result := append([]wire.FoundationKeyCertificate(nil), input...)
	for i := range result {
		result[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), result[i].ReleaseSignatures...)
	}
	return result
}

func vfCloneGenesis(input wire.GenesisManifest) wire.GenesisManifest {
	input.ReleaseKeyIDs = append([]string(nil), input.ReleaseKeyIDs...)
	input.FoundationKeyCertificates = vfCloneCertificates(input.FoundationKeyCertificates)
	input.FeeSchedules = append([]wire.FeeSchedule(nil), input.FeeSchedules...)
	input.FeeScheduleApprovals = append([]wire.FeeScheduleApproval(nil), input.FeeScheduleApprovals...)
	for i := range input.FeeScheduleApprovals {
		input.FeeScheduleApprovals[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), input.FeeScheduleApprovals[i].ReleaseSignatures...)
	}
	input.DistributionPolicy.Monitors = append([]string(nil), input.DistributionPolicy.Monitors...)
	input.DistributionPolicy.ReleaseSignatures = append([]wire.ReleaseSignature(nil), input.DistributionPolicy.ReleaseSignatures...)
	input.ReleaseSignatures = append([]wire.ReleaseSignature(nil), input.ReleaseSignatures...)
	return input
}

func vfCloneClosures(input []wire.FoundationKeyClosure) []wire.FoundationKeyClosure {
	result := append([]wire.FoundationKeyClosure(nil), input...)
	for i := range result {
		result[i].Voided = append([]string(nil), result[i].Voided...)
		result[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), result[i].ReleaseSignatures...)
	}
	return result
}

func (t *TrustRoot) vfApproveGenesisSchedules(g wire.GenesisManifest) {
	for _, schedule := range g.FeeSchedules {
		var approval wire.FeeScheduleApproval
		for _, candidate := range g.FeeScheduleApprovals {
			if candidate.ScheduleID == schedule.ScheduleID {
				approval = candidate
				break
			}
		}
		t.vfApproveSchedule(schedule, approval)
	}
}

func (t *TrustRoot) vfApproveSchedule(s wire.FeeSchedule, a wire.FeeScheduleApproval) {
	var previous *wire.FeeSchedule
	for i := range t.schedules {
		if t.schedules[i].Network == s.Network && t.schedules[i].Revision == s.Revision {
			t.rejected = append(t.rejected, RejectedObject{Kind: "fee_schedule", ID: s.ScheduleID, Reason: "duplicate network revision"})
			return
		}
		if t.schedules[i].Network == s.Network && t.schedules[i].Revision+1 == s.Revision {
			previous = &t.schedules[i]
		}
	}
	approvedAt, err := wire.ParseTime(a.ApprovedAt)
	if err == nil {
		set, setErr := t.timeline.SetAt(approvedAt, a)
		if setErr == nil {
			err = fee.VerifyApproval(s, a, set.IDs, set.Threshold, previous)
		} else {
			err = setErr
		}
	} else {
		err = errors.New("invalid approval time")
	}
	if err != nil {
		t.rejected = append(t.rejected, RejectedObject{Kind: "fee_schedule", ID: s.ScheduleID, Reason: err.Error()})
		return
	}
	t.schedules = append(t.schedules, s)
}

func (t *TrustRoot) vfPruneUnpairedRevisions() {
	for {
		i, reason := t.vfFirstUnpairedRevision()
		if i < 0 {
			return
		}
		t.rejected = append(t.rejected, RejectedObject{Kind: "fee_schedule", ID: t.schedules[i].ScheduleID, Reason: reason})
		t.schedules = append(t.schedules[:i], t.schedules[i+1:]...)
	}
}

func (t *TrustRoot) vfFirstUnpairedRevision() (int, string) {
	for i, s := range t.schedules {
		if s.Revision <= 1 {
			continue
		}
		if !vfHasPredecessor(s, t.schedules) {
			return i, "previous revision rejected"
		}
		if err := fee.CheckScheduleSet(vfWithPartners(s, t.schedules)); err != nil {
			return i, err.Error()
		}
	}
	return -1, ""
}

func vfHasPredecessor(s wire.FeeSchedule, schedules []wire.FeeSchedule) bool {
	for _, candidate := range schedules {
		if candidate.Network == s.Network && candidate.Revision+1 == s.Revision {
			return true
		}
	}
	return false
}

func vfWithPartners(s wire.FeeSchedule, pool []wire.FeeSchedule) []wire.FeeSchedule {
	set := []wire.FeeSchedule{s}
	for _, candidate := range pool {
		if candidate.Network != s.Network && candidate.EffectiveAt == s.EffectiveAt {
			set = append(set, candidate)
		}
	}
	return set
}

func (t *TrustRoot) vfApprovePolicy(p wire.DistributionPolicy) {
	if t.trust == nil {
		t.rejected = append(t.rejected, RejectedObject{Kind: "distribution_policy", ID: p.PolicyID, Reason: "Foundation trust unavailable"})
		return
	}
	if err := distribution.CheckPolicy(p, t.policies, t.trust); err != nil {
		t.rejected = append(t.rejected, RejectedObject{Kind: "distribution_policy", ID: p.PolicyID, Reason: err.Error()})
		return
	}
	p.Monitors = append([]string(nil), p.Monitors...)
	p.ReleaseSignatures = append([]wire.ReleaseSignature(nil), p.ReleaseSignatures...)
	t.policies = append(t.policies, p)
}

// ScheduleInForce returns the latest approved schedule effective by at.
func (t *TrustRoot) ScheduleInForce(network string, at time.Time) (wire.FeeSchedule, bool) {
	if t == nil {
		return wire.FeeSchedule{}, false
	}
	s, err := fee.InForce(t.schedules, network, at)
	return s, err == nil
}

// ScheduleForReceipt checks an approved schedule against the receipt-time leaf rule.
func (t *TrustRoot) ScheduleForReceipt(scheduleID, network string, issuedAt time.Time) (wire.FeeSchedule, error) {
	if t == nil {
		return wire.FeeSchedule{}, errors.New("nil trust root")
	}
	start := issuedAt.Add(-time.Hour)
	for _, s := range t.schedules {
		if s.ScheduleID != scheduleID || s.Network != network {
			continue
		}
		effective, err := wire.ParseTime(s.EffectiveAt)
		if err != nil || effective.After(issuedAt) {
			break
		}
		if !effective.Before(start) {
			return s, nil
		}
		inForce, ok := t.ScheduleInForce(network, start)
		if ok && inForce.ScheduleID == scheduleID {
			return s, nil
		}
		continue
	}
	return wire.FeeSchedule{}, fmt.Errorf("schedule %q was not approved and in force for %s at receipt time", scheduleID, network)
}

// Committee returns the policy in force and its predecessor during the handover window.
func (t *TrustRoot) Committee(at time.Time) CommitteeView {
	if t == nil {
		return CommitteeView{}
	}
	p, ok := distribution.InForce(t.policies, at)
	if !ok {
		return CommitteeView{}
	}
	view := CommitteeView{Current: append([]string(nil), p.Monitors...), PolicyID: p.PolicyID}
	effective, _ := wire.ParseTime(p.EffectiveAt)
	for i := range t.policies {
		if t.policies[i].PolicyID == p.PolicyID && i > 0 {
			if at.Before(effective.Add(30 * 24 * time.Hour)) {
				view.Previous = append([]string(nil), t.policies[i-1].Monitors...)
				view.ChangedAt = effective
			}
			break
		}
	}
	return view
}

// VerifyFoundationObject verifies a Foundation signature and any closure-log requirement.
func (t *TrustRoot) VerifyFoundationObject(obj any, signedAt time.Time, label string, proof *wire.FoundationLogProof) error {
	if t == nil {
		return errors.New("nil trust root")
	}
	return t.trust.VerifyFoundation(obj, signedAt, label, proof)
}

// VerifyVaultReceipt checks the message binding and Foundation signature of a vault receipt.
func (t *TrustRoot) VerifyVaultReceipt(r wire.VaultReceipt, messageID string, proof *wire.FoundationLogProof) error {
	if r.MessageID != messageID {
		return errors.New("vault receipt message_id mismatch")
	}
	at, err := wire.ParseTime(r.StoredAt)
	if err != nil {
		return err
	}
	return t.VerifyFoundationObject(r, at, "vault-receipt", proof)
}

// Rejected returns a copy of invalid or unapproved Foundation objects.
func (t *TrustRoot) Rejected() []RejectedObject {
	if t == nil {
		return nil
	}
	return append([]RejectedObject(nil), t.rejected...)
}

// Summary returns display data for this trust root.
func (t *TrustRoot) Summary() TrustSummary {
	if t == nil {
		return TrustSummary{}
	}
	s := TrustSummary{ReleaseKeyIDs: append([]string(nil), t.pin.ReleaseKeyIDs...), Threshold: t.pin.Threshold, Closures: vfCloneClosures(t.closures), Rejected: t.Rejected(), Committee: t.Committee(t.fetchedAt), Endpoint: t.endpoint, FetchedAt: t.fetchedAt}
	for _, c := range t.certificates {
		s.Foundations = append(s.Foundations, FoundationSummary{c.FoundationID, c.ValidFrom, c.ValidUntil, c.Recipients})
	}
	for _, f := range t.schedules {
		s.Schedules = append(s.Schedules, ScheduleSummary{f.ScheduleID, f.Network, f.Revision, f.Denomination, f.BaseFeeBaseUnits, f.PerKiBFeeBaseUnits, f.EffectiveAt, f.ExpiresAt})
	}
	if p, ok := distribution.InForce(t.policies, t.fetchedAt); ok {
		s.CommitteeEffectiveAt = p.EffectiveAt
	}
	return s
}

func vfEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
