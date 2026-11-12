package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	foundationWatchOrder    = 50
	watchStateCollection    = "monitor.watch.state"
	watchEntryCollection    = "monitor.watch.entries"
	watchHeadCollection     = "monitor.watch.heads"
	watchEvidenceCollection = "monitor.watch.evidence"
)

// watchEntryLimit and watchTransferLimit bound work in one scheduler pass.
var watchEntryLimit = 1000
var watchTransferLimit = 1000

var (
	watchStateValues    = store.Collection[watchState]{Name: watchStateCollection}
	watchEntryValues    = store.Collection[wire.FoundationLogEntry]{Name: watchEntryCollection}
	watchHeadValues     = store.Collection[watchHeldHead]{Name: watchHeadCollection}
	watchEvidenceValues = store.Collection[Evidence]{Name: watchEvidenceCollection}
)

// Evidence is durable evidence of an inconsistency or recipient-funded deposit.
type Evidence struct {
	Kind       string            `json:"kind"`        // Kind is bad_entry, missing_object, rewritten_log, head_after_closure or recipient_funded_deposit.
	ObservedAt time.Time         `json:"observed_at"` // ObservedAt is when this process first recorded the evidence.
	Objects    []json.RawMessage `json:"objects"`     // Objects holds the exact signed objects or transfer involved.
}

type watchState struct {
	NextIndex     uint64               `json:"next_index"`
	AfterHeadSize uint64               `json:"after_head_size"`
	TransferAfter map[string]uint64    `json:"transfer_after"`
	Closures      map[string]time.Time `json:"closures"`
}

type watchHeldHead struct {
	Head       wire.FoundationLogHead `json:"head"`
	ReceivedAt time.Time              `json:"received_at"`
}

func init() {
	loopSteps = append(loopSteps, loopStep{order: foundationWatchOrder, name: "foundation watch", run: watchFoundation})
}

// FoundationEvidence returns a copy of all durably recorded Foundation evidence.
func (s *Service) FoundationEvidence() []Evidence {
	var evidence []Evidence
	_ = s.DB().View(func(tx store.Tx) error {
		return watchEvidenceValues.Scan(tx, store.Range{}, func(_ string, item Evidence) (bool, error) {
			objects := make([]json.RawMessage, len(item.Objects))
			for i, object := range item.Objects {
				objects[i] = append(json.RawMessage(nil), object...)
			}
			item.Objects = objects
			evidence = append(evidence, item)
			return true, nil
		})
	})
	return evidence
}

func watchFoundation(s *Service, parent context.Context) error {
	active := false
	for _, member := range s.Members() {
		if s.View().IsMember(member.DID(), s.Now()) {
			active = true
			break
		}
	}
	if !active {
		return nil
	}
	state, err := watchLoadState(s)
	if err != nil {
		return err
	}
	baseTrust := s.View().Trust()
	foundationID, ok := alFoundationID(s)
	if !ok {
		log.Printf("monitor: foundation watch: no current certified Foundation key")
		return nil
	}
	client, err := sdk.New(s.VaultURL(), sdk.Options{ServiceID: foundationID, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.Now})
	if err != nil {
		log.Printf("monitor: foundation watch: Foundation unavailable: %v", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	certPage, _, certErr := client.Certificates(ctx)
	trust := baseTrust
	certificates := s.config.Genesis.FoundationKeyCertificates
	if certErr == nil {
		certificates = certPage.Certificates
		trust, err = governance.NewTrust(s.config.Genesis, certPage.KeySetUpdates, certPage.Certificates, certPage.Closures, nil)
		if err != nil {
			log.Printf("monitor: foundation watch: invalid certificates page: %v", err)
			trust = baseTrust
			certificates = s.config.Genesis.FoundationKeyCertificates
			certErr = err
		}
	} else {
		log.Printf("monitor: foundation watch: certificates fetch failed: %v", certErr)
	}
	if err := watchMirrorLog(s, ctx, client, &state, certificates); err != nil {
		return err
	}
	// Head and transfer cursors move past what was read, so they wait for current certificates
	// instead of skipping keys that only the genesis fallback cannot certify.
	if certErr != nil {
		return watchCheckConsistency(s)
	}
	if err := watchFetchHeads(s, ctx, client, &state, certificates); err != nil {
		return err
	}
	if err := watchFetchClosures(s, &state, certPage, trust); err != nil {
		return err
	}
	if err := watchCheckConsistency(s); err != nil {
		return err
	}
	return watchTransfers(s, parent, &state, certificates)
}

func watchLoadState(s *Service) (watchState, error) {
	state := watchState{TransferAfter: map[string]uint64{}, Closures: map[string]time.Time{}}
	err := s.DB().View(func(tx store.Tx) error {
		stored, ok, err := watchStateValues.Get(tx, "current")
		if err != nil || !ok {
			return err
		}
		state = stored
		if state.TransferAfter == nil {
			state.TransferAfter = map[string]uint64{}
		}
		if state.Closures == nil {
			state.Closures = map[string]time.Time{}
		}
		return nil
	})
	return state, err
}

func watchPersistState(s *Service, state watchState) error {
	return s.DB().Update(func(tx store.Tx) error { return watchStateValues.Put(tx, "current", state) })
}

func watchAfterPointer(index uint64) *uint64 {
	if index == 0 {
		return nil
	}
	previous := index - 1
	return &previous
}

func watchValidKind(kind string) bool {
	return kind == string(governance.LogKindVaultReceipt) || kind == string(governance.LogKindStatement) || kind == string(governance.LogKindVaultStatus)
}

func watchMirrorLog(s *Service, ctx context.Context, client *sdk.Client, state *watchState, certificates []wire.FoundationKeyCertificate) error {
	processed := 0
	for processed < watchEntryLimit {
		page, _, err := client.Log(ctx, watchAfterPointer(state.NextIndex))
		if err != nil {
			log.Printf("monitor: foundation watch: log fetch failed: %v", err)
			return nil
		}
		if page.Head != nil {
			if err := watchKeepHead(s, *page.Head, certificates); err != nil {
				return err
			}
		}
		for _, entry := range page.Entries {
			if uint64(entry.Index) != state.NextIndex || !watchValidKind(entry.Kind) {
				if err := watchRecord(s, "bad_entry", []any{entry}); err != nil {
					return err
				}
				return watchPersistState(s, *state)
			}
			_, _, objectErr := client.LogObject(ctx, entry.ObjectHash)
			if objectErr != nil {
				log.Printf("monitor: foundation watch: object %s failed validation: %v", entry.ObjectHash, objectErr)
				var publicError *sdk.Error
				if errors.As(objectErr, &publicError) && publicError.HTTPStatus == 404 || sdk.Classify(objectErr) == sdk.SignalProtocol {
					if err := watchRecord(s, "missing_object", []any{entry}); err != nil {
						return err
					}
				} else {
					return nil
				}
			}
			if err := s.DB().Update(func(tx store.Tx) error {
				if err := watchEntryValues.Put(tx, fmt.Sprintf("%020d", entry.Index), entry); err != nil {
					return err
				}
				state.NextIndex++
				return watchStateValues.Put(tx, "current", *state)
			}); err != nil {
				return err
			}
			processed++
			if processed == watchEntryLimit {
				break
			}
		}
		if len(page.Entries) < 1000 || processed == watchEntryLimit {
			break
		}
	}
	return watchPersistState(s, *state)
}

func watchKeepHead(s *Service, head wire.FoundationLogHead, certificates []wire.FoundationKeyCertificate) error {
	if governance.VerifyLogHead(head) != nil || !watchHeadCertified(head, certificates) {
		return nil
	}
	raw, err := canon.Encode(head)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	key := head.FoundationID + "/" + strconv.FormatInt(head.Size, 10) + "/" + head.Root + "/" + hex.EncodeToString(digest[:])
	return s.DB().Update(func(tx store.Tx) error {
		if _, ok, err := watchHeadValues.Get(tx, key); err != nil || ok {
			return err
		}
		return watchHeadValues.Put(tx, key, watchHeldHead{Head: head, ReceivedAt: s.Now()})
	})
}

func watchHeadCertified(head wire.FoundationLogHead, certificates []wire.FoundationKeyCertificate) bool {
	at, err := wire.ParseTime(head.SignedAt)
	if err != nil {
		return false
	}
	for _, certificate := range certificates {
		from, fromErr := wire.ParseTime(certificate.ValidFrom)
		until, untilErr := wire.ParseTime(certificate.ValidUntil)
		if fromErr == nil && untilErr == nil && certificate.FoundationID == head.FoundationID && !at.Before(from) && at.Before(until) {
			return true
		}
	}
	return false
}

func watchFetchHeads(s *Service, ctx context.Context, client *sdk.Client, state *watchState, certificates []wire.FoundationKeyCertificate) error {
	for pages := 0; pages < watchEntryLimit; pages++ {
		page, _, err := client.LogHeads(ctx, state.AfterHeadSize)
		if err != nil {
			log.Printf("monitor: foundation watch: heads fetch failed: %v", err)
			return nil
		}
		if len(page.Heads) == 0 {
			break
		}
		previous := state.AfterHeadSize
		for _, head := range page.Heads {
			if head.Size <= 0 {
				continue
			}
			if err := watchKeepHead(s, head, certificates); err != nil {
				return err
			}
			if uint64(head.Size) > state.AfterHeadSize {
				state.AfterHeadSize = uint64(head.Size)
			}
		}
		if err := watchPersistState(s, *state); err != nil {
			return err
		}
		if len(page.Heads) < 1000 || state.AfterHeadSize == previous {
			break
		}
	}
	return nil
}

func watchFetchClosures(s *Service, state *watchState, page wire.FoundationCertificatesPage, trust *governance.Trust) error {
	for _, closure := range page.Closures {
		closedAt, err := wire.ParseTime(closure.ClosedAt)
		if err != nil || governance.VerifyLogHead(closure.Head) != nil {
			continue
		}
		unsigned, err := canon.EncodeWithout(closure, "release_signatures")
		if err != nil {
			return err
		}
		parsed, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
		if err != nil || trust.VerifyRelease("foundation-key-closure", parsed, closure.ReleaseSignatures, closedAt, closure) != nil {
			continue
		}
		canonicalClosure, err := canon.Encode(closure)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonicalClosure)
		closureKey := hex.EncodeToString(digest[:])
		firstSeen, seen := state.Closures[closureKey]
		if !seen {
			firstSeen = s.Now()
			state.Closures[closureKey] = firstSeen
		}
		var heads []watchHeldHead
		if err := s.DB().View(func(tx store.Tx) error {
			return watchHeadValues.Scan(tx, store.Range{}, func(_ string, h watchHeldHead) (bool, error) { heads = append(heads, h); return true, nil })
		}); err != nil {
			return err
		}
		for _, held := range heads {
			if held.Head.FoundationID == closure.FoundationID && held.Head.Size > closure.Head.Size && held.ReceivedAt.Before(firstSeen) {
				if err := watchRecord(s, "head_after_closure", []any{closure, held.Head}); err != nil {
					return err
				}
			}
		}
	}
	return watchPersistState(s, *state)
}

func watchCheckConsistency(s *Service) error {
	var entries []wire.FoundationLogEntry
	var heads []watchHeldHead
	if err := s.DB().View(func(tx store.Tx) error {
		if err := watchEntryValues.Scan(tx, store.Range{}, func(_ string, e wire.FoundationLogEntry) (bool, error) {
			entries = append(entries, e)
			return true, nil
		}); err != nil {
			return err
		}
		return watchHeadValues.Scan(tx, store.Range{}, func(_ string, h watchHeldHead) (bool, error) { heads = append(heads, h); return true, nil })
	}); err != nil {
		return err
	}
	mirrored := make([]string, len(entries))
	for i, entry := range entries {
		mirrored[i] = entry.ObjectHash
	}
	sort.Slice(heads, func(i, j int) bool {
		if heads[i].Head.FoundationID != heads[j].Head.FoundationID {
			return heads[i].Head.FoundationID < heads[j].Head.FoundationID
		}
		if heads[i].Head.Size != heads[j].Head.Size {
			return heads[i].Head.Size < heads[j].Head.Size
		}
		return heads[i].Head.Root < heads[j].Head.Root
	})
	paired := make([]bool, len(heads))
	for i := 0; i < len(heads); i++ {
		for j := i + 1; j < len(heads); j++ {
			first, second := heads[i].Head, heads[j].Head
			if first.FoundationID != second.FoundationID {
				continue
			}
			if first.Size == second.Size && first.Root != second.Root {
				paired[i], paired[j] = true, true
				if err := watchRecord(s, "rewritten_log", []any{first, second}); err != nil {
					return err
				}
				continue
			}
			a, b := first, second
			if a.Size < b.Size {
				a, b = b, a
			}
			if b.Size <= a.Size && a.Size <= int64(len(mirrored)) && governance.HeadsConsistent(mirrored, mirrored, a, a) && !governance.HeadsConsistent(mirrored, mirrored, b, b) {
				paired[i], paired[j] = true, true
				if err := watchRecord(s, "rewritten_log", []any{first, second}); err != nil {
					return err
				}
			}
		}
	}
	for i, held := range heads {
		head := held.Head
		if !paired[i] && head.Size <= int64(len(mirrored)) && !governance.HeadsConsistent(mirrored, mirrored, head, head) {
			if err := watchRecord(s, "rewritten_log", []any{head}); err != nil {
				return err
			}
		}
	}
	return nil
}

func watchTransfers(s *Service, parent context.Context, state *watchState, certificates []wire.FoundationKeyCertificate) error {
	networkSet := make(map[string]bool)
	for _, certificate := range certificates {
		if certificate.Recipients.BitcoinMainnet != "" {
			networkSet[faux.BitcoinMainnet] = true
		}
		if certificate.Recipients.EthereumMainnet != "" {
			networkSet[faux.EthereumMainnet] = true
		}
	}
	networks := make([]string, 0, len(networkSet))
	for network := range networkSet {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	providers := watchProviderFirstKeys(s.Peers())
	commitments := make(map[string]string)
	for _, firstKey := range providers {
		for _, network := range networks {
			commitment, err := settlement.DepositCommitment(firstKey, network)
			if err == nil {
				commitments[network+"/"+hex.EncodeToString(commitment[:])] = firstKey
			}
		}
	}
	processed := 0
	for _, network := range networks {
		remaining := watchTransferLimit - processed
		if remaining <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		transfers, err := s.config.Faux.Transfers(ctx, faux.Query{Network: network, After: state.TransferAfter[network], Limit: remaining})
		cancel()
		if err != nil {
			log.Printf("monitor: foundation watch: faux transfers unavailable: %v", err)
			return nil
		}
		for _, transfer := range transfers {
			processed++
			state.TransferAfter[network] = transfer.Sequence
			data, decodeErr := hex.DecodeString(transfer.Data)
			if decodeErr == nil && len(data) == 32 {
				recipient, from := false, false
				for _, certificate := range certificates {
					certRecipient := watchRecipient(certificate, network)
					if certRecipient != "" && transfer.Recipient == certRecipient {
						recipient = true
					}
					if certRecipient != "" && transfer.From == certRecipient {
						from = true
					}
				}
				if recipient && from {
					matching := commitments[network+"/"+hex.EncodeToString(data)]
					objects := []any{transfer, struct {
						From             string `json:"from"`
						Commitment       string `json:"commitment"`
						ProviderFirstKey string `json:"provider_first_key,omitempty"`
					}{transfer.From, hex.EncodeToString(data), matching}}
					if err := watchRecord(s, "recipient_funded_deposit", objects); err != nil {
						return err
					}
				}
			}
			if processed >= watchTransferLimit {
				break
			}
		}
	}
	return watchPersistState(s, *state)
}

// watchProviderFirstKeys maps configured provider peer names to devnet first keys ("provider" is
// provider 0, "provider{n}" is provider n), because the core config names providers only by URL.
func watchProviderFirstKeys(peers map[string]string) []string {
	var firstKeys []string
	for name := range peers {
		suffix, ok := strings.CutPrefix(name, "provider")
		if !ok {
			continue
		}
		n := 0
		if suffix != "" {
			parsed, err := strconv.Atoi(suffix)
			if err != nil || parsed < 0 || strconv.Itoa(parsed) != suffix {
				continue
			}
			n = parsed
		}
		firstKeys = append(firstKeys, keys.DIDOf(devnet.ProviderKey(n)))
	}
	return firstKeys
}

func watchRecipient(c wire.FoundationKeyCertificate, network string) string {
	switch network {
	case faux.BitcoinMainnet:
		return c.Recipients.BitcoinMainnet
	case faux.EthereumMainnet:
		return c.Recipients.EthereumMainnet
	default:
		return ""
	}
}

func watchRecord(s *Service, kind string, objects []any) error {
	rawObjects := make([]json.RawMessage, 0, len(objects))
	for _, object := range objects {
		encoded, err := canon.Encode(object)
		if err != nil {
			return err
		}
		rawObjects = append(rawObjects, encoded)
	}
	identity, err := canon.Encode(struct {
		Kind    string            `json:"kind"`
		Objects []json.RawMessage `json:"objects"`
	}{kind, rawObjects})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(identity)
	key := hex.EncodeToString(digest[:])
	return s.DB().Update(func(tx store.Tx) error {
		if _, exists, err := watchEvidenceValues.Get(tx, key); err != nil || exists {
			return err
		}
		record := Evidence{Kind: kind, ObservedAt: s.Now(), Objects: rawObjects}
		if err := watchEvidenceValues.Put(tx, key, record); err != nil {
			return err
		}
		log.Printf("monitor: ERROR foundation evidence %s %s", kind, key)
		return nil
	})
}
