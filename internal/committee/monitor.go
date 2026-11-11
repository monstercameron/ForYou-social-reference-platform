// Package committee implements one settlement monitor as a deterministic state machine.
package committee

import (
	"errors"
	"math/big"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// DepositSource provides the counted settlement deposits for a provider line.
type DepositSource interface {
	Deposited(firstKey, network string) (*big.Int, error)
}

// Config supplies the monitor identity and its read-only audit sources. Schedules may expose a growing verified set.
type Config struct {
	Key       keys.PrivateKey
	Schedules settlement.Schedules
	Deposits  DepositSource
}

// Monitor holds one committee member's local settlement decisions. Calls must be serialized.
type Monitor struct {
	key       keys.PrivateKey
	schedules settlement.Schedules
	deposits  DepositSource
	line      *succession.ProviderLine
	state     monitorState
}

// New creates an empty deterministic settlement monitor.
func New(cfg Config) (*Monitor, error) {
	if len(cfg.Key) == 0 || keys.DIDOf(cfg.Key) == "" || cfg.Schedules == nil || cfg.Deposits == nil {
		return nil, errors.New("committee: key, schedules and deposits are required")
	}
	return &Monitor{key: append(keys.PrivateKey(nil), cfg.Key...), schedules: cfg.Schedules, deposits: cfg.Deposits, line: succession.NewProviderLine(), state: newMonitorState()}, nil
}

// ID returns this monitor's did:key identifier.
func (m *Monitor) ID() string { return keys.DIDOf(m.key) }

// Line returns the first key in the accepted succession line containing providerID.
func (m *Monitor) Line(providerID string) string {
	for _, s := range m.state.Successions {
		line := m.line.Line(s.OldID)
		for _, id := range line {
			if id == providerID {
				return line[0]
			}
		}
	}
	return providerID
}

// RegisterSuccession verifies and accepts a provider key succession.
func (m *Monitor) RegisterSuccession(s wire.KeySuccession) error {
	if err := succession.Verify(s); err != nil {
		return err
	}
	if _, ok := m.line.ByID(s.SuccessionID); ok {
		return nil
	}
	err := m.line.Accept(s, func(id string) bool {
		return m.hasSignedClaim(id)
	})
	if err != nil {
		return err
	}
	m.state.Successions = append(m.state.Successions, s)
	m.markDataAvailable(dependencySuccession)
	return nil
}

// RegisterDeclaration verifies and holds a provider retention declaration.
func (m *Monitor) RegisterDeclaration(d wire.RetentionDeclaration) error {
	if err := retention.Verify(d); err != nil {
		return err
	}
	m.state.Declarations[d.DeclarationID] = d
	m.markDataAvailable(dependencyDeclaration)
	return nil
}

// Observe records the first time this monitor saw a claim ID.
func (m *Monitor) Observe(claimID string, at time.Time) time.Time {
	if value, ok := m.state.FirstSeen[claimID]; ok {
		return value
	}
	at = at.UTC().Truncate(time.Second)
	m.state.FirstSeen[claimID] = at
	return at
}

func (m *Monitor) predecessor(c wire.SettlementClaim) *wire.SettlementClaim {
	if c.Sequence <= 1 {
		return nil
	}
	line := m.Line(c.ProviderID)
	key := slotKey(line, c.Network, c.Sequence-1)
	if followed, ok := m.state.Followed[key]; ok {
		copy := followed
		return &copy
	}
	if witnessed, ok := m.state.Witnessed[key]; ok {
		claim := m.state.Claims[witnessed.ClaimID]
		return &claim
	}
	return nil
}

func (m *Monitor) hasWitnessedClaim(id string) bool {
	for _, witness := range m.state.Witnessed {
		if witness.ClaimID == id {
			return true
		}
	}
	return false
}

func (m *Monitor) hasSignedClaim(id string) bool {
	for _, witness := range m.state.Witnessed {
		claim := m.state.Claims[witness.ClaimID]
		if claim.ProviderID == id {
			return true
		}
	}
	for _, claim := range m.state.Followed {
		if claim.ProviderID == id {
			return true
		}
	}
	return false
}

func (m *Monitor) succession(id string) (wire.KeySuccession, bool) { return m.line.ByID(id) }

func (m *Monitor) deposited(line, network string) (*big.Int, error) {
	return m.deposits.Deposited(line, network)
}
