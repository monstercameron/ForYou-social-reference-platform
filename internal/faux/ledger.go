package faux

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
)

const (
	// BitcoinMainnet is the Bitcoin network identifier used by the protocol.
	BitcoinMainnet = "bitcoin-mainnet"
	// EthereumMainnet is the Ethereum network identifier used by the protocol.
	EthereumMainnet = "ethereum-mainnet"
	// BitcoinConfirmations is the faux Bitcoin depth required for finality.
	BitcoinConfirmations = 6
	// EthereumFinalityDepth is the faux Ethereum depth required for finality.
	EthereumFinalityDepth = 2
	// MaxDataBytes is the maximum transaction data payload.
	MaxDataBytes = 80
)

var (
	// ErrNotFound identifies an unknown transfer.
	ErrNotFound = errors.New("faux: not found")
	// ErrConflict identifies a client_ref reused for different request content.
	ErrConflict = errors.New("faux: conflict")
	// ErrInvalid identifies invalid input.
	ErrInvalid = errors.New("faux: invalid")
	// ErrUnavailable identifies a journal that cannot accept mutations.
	ErrUnavailable = errors.New("faux: unavailable")
	// ErrFull identifies a ledger at its configured transfer limit.
	ErrFull = errors.New("faux: full")
)

const blockTimeLayout = "2006-01-02T15:04:05Z"

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var amountPattern = regexp.MustCompile(`^[1-9][0-9]{0,77}$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var hexPattern = regexp.MustCompile(`^(?:[0-9a-f]{2})*$`)

// Transfer is an observed faux-chain transfer.
type Transfer struct {
	Network          string  `json:"network"`
	TxID             string  `json:"txid,omitempty"`
	OutputIndex      *uint32 `json:"output_index,omitempty"`
	TxHash           string  `json:"tx_hash,omitempty"`
	From             string  `json:"from"`
	Recipient        string  `json:"recipient"`
	Script           string  `json:"script,omitempty"`
	AmountChainUnits string  `json:"amount_chain_units"`
	Data             string  `json:"data"`
	Sequence         uint64  `json:"sequence"`
	BlockHeight      uint64  `json:"block_height"`
	BlockTime        string  `json:"block_time"`
	Confirmations    uint64  `json:"confirmations"`
	Finalized        bool    `json:"finalized"`
	Success          bool    `json:"success"`
	Coinbase         bool    `json:"coinbase"`
	ClientRef        string  `json:"client_ref,omitempty"`
}

// SubmitRequest describes a transfer to mine.
type SubmitRequest struct {
	Network          string `json:"network"`
	From             string `json:"from,omitempty"`
	Recipient        string `json:"recipient"`
	AmountChainUnits string `json:"amount_chain_units"`
	Data             string `json:"data,omitempty"`
	Pending          bool   `json:"pending,omitempty"`
	ClientRef        string `json:"client_ref,omitempty"`
}

// Query filters transfers in sequence order.
type Query struct {
	Network   string
	Recipient string
	Data      string
	After     uint64
	Limit     int
}

// Tip describes the latest block on one network.
type Tip struct {
	Network string `json:"network"`
	Height  uint64 `json:"height"`
	Time    string `json:"time"`
}

// AddressTotals reports informational sent and received totals.
type AddressTotals struct {
	Network   string     `json:"network"`
	Address   string     `json:"address"`
	Received  string     `json:"received"`
	Sent      string     `json:"sent"`
	Transfers []Transfer `json:"transfers"`
}

// Config configures the journaled faux ledger.
type Config struct {
	Now          func() time.Time
	JournalPath  string
	MaxTransfers int
}

// block is one mined block. The first block of a submit record includes that
// record's transfer; every other block is empty.
type block struct {
	Height uint64 `json:"height"`
	Time   string `json:"time"`
}

// journalRecord stores only one mutation so journal growth stays linear.
type journalRecord struct {
	Kind     string         `json:"kind"`
	Network  string         `json:"network,omitempty"`
	Transfer *Transfer      `json:"transfer,omitempty"`
	Request  *SubmitRequest `json:"request,omitempty"`
	Blocks   []block        `json:"blocks"`
}

type refRecord struct {
	request  SubmitRequest
	transfer Transfer
}

// Ledger is a concurrent, journaled faux chain.
type Ledger struct {
	mu          sync.RWMutex
	now         func() time.Time
	path        string
	max         int
	blocks      map[string][]block
	transfers   []Transfer
	refs        map[string]refRecord
	ids         map[string]int
	unavailable bool
}

// Open opens and replays a faux ledger journal.
func Open(cfg Config) (*Ledger, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxTransfers <= 0 {
		cfg.MaxTransfers = 1_000_000
	}
	l := &Ledger{now: cfg.Now, path: cfg.JournalPath, max: cfg.MaxTransfers, blocks: map[string][]block{BitcoinMainnet: {}, EthereumMainnet: {}}, refs: make(map[string]refRecord), ids: make(map[string]int)}
	if cfg.JournalPath == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.JournalPath), 0700); err != nil {
		return nil, err
	}
	f, err := os.Open(cfg.JournalPath)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		var record journalRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("faux: invalid journal: %w", err)
		}
		if err := l.replay(record); err != nil {
			return nil, fmt.Errorf("faux: invalid journal: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return l, nil
}

func supported(network string) bool { return network == BitcoinMainnet || network == EthereumMainnet }

func depth(network string) uint64 {
	if network == BitcoinMainnet {
		return BitcoinConfirmations
	}
	return EthereumFinalityDepth
}

// Submit adds a transfer and immediately confirms it unless pending is requested.
func (l *Ledger) Submit(ctx context.Context, req SubmitRequest) (Transfer, error) {
	t, _, err := l.submit(ctx, req)
	return t, err
}

func (l *Ledger) submit(ctx context.Context, req SubmitRequest) (Transfer, bool, error) {
	if err := ctx.Err(); err != nil {
		return Transfer{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unavailable {
		return Transfer{}, false, ErrUnavailable
	}
	if req.From == "" {
		req.From = DevAddress(req.Network, "anonymous")
	}
	if err := validateSubmitRequest(req); err != nil {
		return Transfer{}, false, err
	}
	if req.ClientRef != "" {
		if prior, ok := l.refs[req.ClientRef]; ok {
			if prior.request != req {
				return Transfer{}, false, ErrConflict
			}
			return l.withConfirmations(prior.transfer), true, nil
		}
	}
	if len(l.transfers) >= l.max {
		return Transfer{}, false, ErrFull
	}
	sequence := uint64(len(l.transfers) + 1)
	id, err := transactionID(req, sequence)
	if err != nil {
		return Transfer{}, false, err
	}
	t := Transfer{Network: req.Network, From: req.From, Recipient: req.Recipient, AmountChainUnits: req.AmountChainUnits, Data: req.Data, Sequence: sequence, Success: true, ClientRef: req.ClientRef}
	if req.Network == BitcoinMainnet {
		index := uint32(0)
		script, _ := BitcoinScript(req.Recipient)
		t.TxID, t.OutputIndex, t.Script = id, &index, hex.EncodeToString(script)
	} else {
		t.TxHash = id
	}
	count := 1
	if !req.Pending {
		count = int(depth(req.Network))
	}
	newBlocks := l.newBlocks(req.Network, count)
	t.BlockHeight, t.BlockTime = newBlocks[0].Height, newBlocks[0].Time
	requestCopy, transferCopy := req, t
	record := journalRecord{Kind: "submit", Network: req.Network, Transfer: &transferCopy, Request: &requestCopy, Blocks: newBlocks}
	if err := l.persist(record); err != nil {
		l.unavailable = true
		return Transfer{}, false, fmt.Errorf("%w: journal write", ErrUnavailable)
	}
	l.apply(record)
	return l.withConfirmations(t), false, nil
}

func (l *Ledger) persist(record journalRecord) error {
	if l.path == "" {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (l *Ledger) replay(record journalRecord) error {
	switch record.Kind {
	case "submit":
		if record.Transfer == nil || record.Request == nil || record.Network != record.Transfer.Network || record.Network != record.Request.Network || len(record.Blocks) == 0 {
			return errors.New("invalid submit record")
		}
		if err := l.validateReplayRequest(*record.Request, *record.Transfer); err != nil {
			return err
		}
		if record.Transfer.BlockHeight != record.Blocks[0].Height || record.Transfer.BlockTime != record.Blocks[0].Time || record.Request.Pending && len(record.Blocks) != 1 || !record.Request.Pending && len(record.Blocks) != int(depth(record.Network)) {
			return errors.New("inconsistent submit block data")
		}
		if len(l.transfers) >= l.max {
			return errors.New("transfer limit exceeded")
		}
		if _, exists := l.ids[record.Network+"\n"+transferID(*record.Transfer)]; exists {
			return errors.New("duplicate transfer ID")
		}
		if record.Request.ClientRef != "" {
			if _, exists := l.refs[record.Request.ClientRef]; exists {
				return errors.New("duplicate client_ref")
			}
		}
	case "mine":
		if !supported(record.Network) || len(record.Blocks) == 0 || len(record.Blocks) > 100 || record.Transfer != nil || record.Request != nil {
			return errors.New("invalid mine record")
		}
	default:
		return errors.New("unknown record kind")
	}
	if err := validateBlocks(l.blocks[record.Network], record.Blocks); err != nil {
		return err
	}
	l.apply(record)
	return nil
}

func (l *Ledger) validateReplayRequest(req SubmitRequest, t Transfer) error {
	if err := validateSubmitRequest(req); err != nil {
		return err
	}
	if req.From == "" || t.Sequence != uint64(len(l.transfers)+1) || t.Network != req.Network || t.From != req.From || t.Recipient != req.Recipient || t.AmountChainUnits != req.AmountChainUnits || t.Data != req.Data || t.ClientRef != req.ClientRef || !t.Success || t.Coinbase || t.Confirmations != 0 || t.Finalized {
		return errors.New("inconsistent submit record")
	}
	id, err := transactionID(req, t.Sequence)
	if err != nil {
		return err
	}
	if req.Network == BitcoinMainnet {
		script, _ := BitcoinScript(req.Recipient)
		if t.TxID != id || t.TxHash != "" || t.OutputIndex == nil || *t.OutputIndex != 0 || t.Script != hex.EncodeToString(script) {
			return errors.New("inconsistent transfer identity")
		}
	} else if t.TxHash != id || t.TxID != "" || t.OutputIndex != nil || t.Script != "" {
		return errors.New("inconsistent transfer identity")
	}
	return nil
}

func validateSubmitRequest(req SubmitRequest) error {
	if !supported(req.Network) {
		return fmt.Errorf("%w: network", ErrInvalid)
	}
	if err := ValidateRecipient(req.Network, req.From); err != nil {
		return err
	}
	if err := ValidateRecipient(req.Network, req.Recipient); err != nil {
		return err
	}
	if !amountPattern.MatchString(req.AmountChainUnits) || req.Network == BitcoinMainnet && (len(req.AmountChainUnits) > 16 || len(req.AmountChainUnits) == 16 && req.AmountChainUnits > "2100000000000000") {
		return fmt.Errorf("%w: amount", ErrInvalid)
	}
	if !hexPattern.MatchString(req.Data) || len(req.Data)/2 > MaxDataBytes {
		return fmt.Errorf("%w: data", ErrInvalid)
	}
	if req.ClientRef != "" && !refPattern.MatchString(req.ClientRef) {
		return fmt.Errorf("%w: client_ref", ErrInvalid)
	}
	return nil
}

// validateBlocks checks that replayed blocks extend chain by consecutive heights
// with canonical, non-decreasing block times.
func validateBlocks(chain, added []block) error {
	height := uint64(len(chain))
	var prior time.Time
	if len(chain) > 0 {
		prior, _ = time.Parse(blockTimeLayout, chain[len(chain)-1].Time)
	}
	for _, b := range added {
		parsed, err := time.Parse(blockTimeLayout, b.Time)
		if err != nil || parsed.Format(blockTimeLayout) != b.Time {
			return errors.New("invalid block time")
		}
		if b.Height != height+1 || parsed.Before(prior) {
			return errors.New("inconsistent block")
		}
		height, prior = b.Height, parsed
	}
	return nil
}

func (l *Ledger) apply(record journalRecord) {
	l.blocks[record.Network] = append(l.blocks[record.Network], record.Blocks...)
	if record.Kind == "submit" {
		t := *record.Transfer
		position := len(l.transfers)
		l.transfers = append(l.transfers, t)
		l.ids[record.Network+"\n"+transferID(t)] = position
		if record.Request.ClientRef != "" {
			l.refs[record.Request.ClientRef] = refRecord{request: *record.Request, transfer: t}
		}
	}
}

// transactionID is the faux transaction ID: SHA-256 over a domain prefix and the
// JCS of the transfer's identity fields. It is a faux convention, not a signing input.
func transactionID(req SubmitRequest, sequence uint64) (string, error) {
	canonical, err := canon.Encode(map[string]any{"network": req.Network, "sequence": sequence, "from": req.From, "recipient": req.Recipient, "amount_chain_units": req.AmountChainUnits, "data": req.Data})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("foryou-faux/tx\n"), canonical...))
	return hex.EncodeToString(digest[:]), nil
}

func transferID(t Transfer) string {
	if t.Network == BitcoinMainnet {
		return t.TxID
	}
	return t.TxHash
}

// newBlocks prepares count blocks after the network's tip without changing the
// ledger; block time is the clock in whole seconds, never before the previous block.
func (l *Ledger) newBlocks(network string, count int) []block {
	chain := l.blocks[network]
	now := l.now().UTC().Truncate(time.Second)
	if len(chain) > 0 {
		previous, _ := time.Parse(blockTimeLayout, chain[len(chain)-1].Time)
		if now.Before(previous) {
			now = previous
		}
	}
	stamp := now.Format(blockTimeLayout)
	blocks := make([]block, count)
	for i := range blocks {
		blocks[i] = block{Height: uint64(len(chain) + i + 1), Time: stamp}
	}
	return blocks
}

// withConfirmations returns a copy of t that shares no memory with the ledger,
// with confirmations and finality computed against the current tip.
func (l *Ledger) withConfirmations(t Transfer) Transfer {
	if t.OutputIndex != nil {
		index := *t.OutputIndex
		t.OutputIndex = &index
	}
	tip := uint64(len(l.blocks[t.Network]))
	if tip >= t.BlockHeight {
		t.Confirmations = tip - t.BlockHeight + 1
	}
	t.Finalized = t.Confirmations >= depth(t.Network)
	return t
}

// Transfer returns one transfer by network and transaction ID.
func (l *Ledger) Transfer(ctx context.Context, network, id string) (Transfer, error) {
	if err := ctx.Err(); err != nil {
		return Transfer{}, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !supported(network) {
		return Transfer{}, fmt.Errorf("%w: network", ErrInvalid)
	}
	if !hashPattern.MatchString(id) {
		return Transfer{}, fmt.Errorf("%w: transfer ID", ErrInvalid)
	}
	position, ok := l.ids[network+"\n"+id]
	if !ok {
		return Transfer{}, ErrNotFound
	}
	return l.withConfirmations(l.transfers[position]), nil
}

// Transfers lists transfers in sequence order after the supplied cursor.
func (l *Ledger) Transfers(ctx context.Context, q Query) ([]Transfer, error) {
	return l.transfersAfter(ctx, q, false)
}

func (l *Ledger) transfersAfter(ctx context.Context, q Query, allowExtra bool) ([]Transfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !supported(q.Network) {
		return nil, fmt.Errorf("%w: network", ErrInvalid)
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	maximum := 1000
	if allowExtra {
		maximum++
	}
	if limit < 1 || limit > maximum {
		return nil, fmt.Errorf("%w: limit", ErrInvalid)
	}
	out := make([]Transfer, 0, limit)
	start := q.After
	if start < uint64(len(l.transfers)) {
		for i := int(start); i < len(l.transfers); i++ {
			t := l.transfers[i]
			if t.Network == q.Network && (q.Recipient == "" || t.Recipient == q.Recipient) && (q.Data == "" || t.Data == q.Data) {
				out = append(out, l.withConfirmations(t))
				if len(out) == limit {
					break
				}
			}
		}
	}
	return out, nil
}

// Mine appends empty blocks to a network.
func (l *Ledger) Mine(ctx context.Context, network string, count int) (Tip, error) {
	if err := ctx.Err(); err != nil {
		return Tip{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !supported(network) {
		return Tip{}, fmt.Errorf("%w: network", ErrInvalid)
	}
	if l.unavailable {
		return Tip{}, ErrUnavailable
	}
	if count < 1 || count > 100 {
		return Tip{}, fmt.Errorf("%w: count", ErrInvalid)
	}
	record := journalRecord{Kind: "mine", Network: network, Blocks: l.newBlocks(network, count)}
	if err := l.persist(record); err != nil {
		l.unavailable = true
		return Tip{}, fmt.Errorf("%w: journal write", ErrUnavailable)
	}
	l.apply(record)
	return tip(network, l.blocks[network]), nil
}

// Tip returns the current tip of a network.
func (l *Ledger) Tip(ctx context.Context, network string) (Tip, error) {
	if err := ctx.Err(); err != nil {
		return Tip{}, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !supported(network) {
		return Tip{}, fmt.Errorf("%w: network", ErrInvalid)
	}
	return tip(network, l.blocks[network]), nil
}

func tip(network string, blocks []block) Tip {
	if len(blocks) == 0 {
		return Tip{Network: network}
	}
	b := blocks[len(blocks)-1]
	return Tip{Network: network, Height: b.Height, Time: b.Time}
}

// Faucet submits from the deterministic dev faucet address.
func (l *Ledger) Faucet(ctx context.Context, network, recipient, amount string) (Transfer, error) {
	if amount == "" {
		if network == BitcoinMainnet {
			amount = "1000000"
		} else {
			amount = "1000000000000000000"
		}
	}
	return l.Submit(ctx, SubmitRequest{Network: network, From: DevAddress(network, "faucet"), Recipient: recipient, AmountChainUnits: amount})
}

// Address returns informational totals and transfers associated with an address.
func (l *Ledger) Address(ctx context.Context, network, address string) (AddressTotals, error) {
	if err := ctx.Err(); err != nil {
		return AddressTotals{}, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !supported(network) {
		return AddressTotals{}, fmt.Errorf("%w: network", ErrInvalid)
	}
	if err := ValidateRecipient(network, address); err != nil {
		return AddressTotals{}, err
	}
	received, sent := new(big.Int), new(big.Int)
	items := make([]Transfer, 0)
	for _, t := range l.transfers {
		if t.Network != network {
			continue
		}
		if t.Recipient == address {
			amount, _ := new(big.Int).SetString(t.AmountChainUnits, 10)
			received.Add(received, amount)
			items = append(items, l.withConfirmations(t))
		}
		if t.From == address {
			amount, _ := new(big.Int).SetString(t.AmountChainUnits, 10)
			sent.Add(sent, amount)
			if t.Recipient != address {
				items = append(items, l.withConfirmations(t))
			}
		}
	}
	return AddressTotals{Network: network, Address: address, Received: received.String(), Sent: sent.String(), Transfers: items}, nil
}
