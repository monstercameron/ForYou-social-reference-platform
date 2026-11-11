package provider

import (
	"encoding/json"
	"errors"

	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

func (s *Service) ensureDeclaration() error {
	var ds []wire.RetentionDeclaration
	if err := s.Store().View(func(tx store.Tx) error {
		return tx.Scan("provider.retention.declarations", store.Range{}, func(_ string, b []byte) (bool, error) {
			var d wire.RetentionDeclaration
			if e := json.Unmarshal(b, &d); e != nil {
				return false, e
			}
			ds = append(ds, d)
			return true, nil
		})
	}); err != nil {
		return err
	}
	if len(ds) > 0 {
		for _, declaration := range ds {
			if err := retention.Verify(declaration); err != nil {
				return err
			}
			if declaration.ProviderID != s.ProviderID() {
				return errors.New("provider: stored declaration provider mismatch")
			}
		}
		return nil
	}
	d, err := retention.Declare(s.cfg.Key, s.cfg.Retention.Seconds, s.cfg.Retention.Payout, s.Now())
	if err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.Store().Update(func(tx store.Tx) error { return tx.Put("provider.retention.declarations", d.DeclarationID, b) })
}

// Declarations returns all stored declarations newest first.
func (s *Service) Declarations() []wire.RetentionDeclaration {
	var ds []wire.RetentionDeclaration
	_ = s.Store().View(func(tx store.Tx) error {
		return tx.Scan("provider.retention.declarations", store.Range{}, func(_ string, b []byte) (bool, error) {
			var d wire.RetentionDeclaration
			if err := json.Unmarshal(b, &d); err != nil {
				return false, err
			}
			ds = append(ds, d)
			return true, nil
		})
	})
	return retention.NewestFirst(ds)
}

// CurrentDeclaration returns the newest declaration effective at the provider's current time.
func (s *Service) CurrentDeclaration() (wire.RetentionDeclaration, bool) {
	now := s.Now()
	for _, d := range s.Declarations() {
		at, err := wire.ParseTime(d.EffectiveAt)
		if err == nil && !at.After(now) {
			return d, true
		}
	}
	return wire.RetentionDeclaration{}, false
}

// AddDeclaration verifies and durably stores a later provider retention declaration.
func (s *Service) AddDeclaration(d wire.RetentionDeclaration) error {
	if err := retention.Verify(d); err != nil {
		return err
	}
	if d.ProviderID != s.ProviderID() {
		return errors.New("provider: declaration provider mismatch")
	}
	at, err := wire.ParseTime(d.EffectiveAt)
	if err != nil || at.Before(s.Now()) {
		return errors.New("provider: declaration effective time is in the past")
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.Store().Update(func(tx store.Tx) error {
		if _, ok, e := tx.Get("provider.retention.declarations", d.DeclarationID); e != nil {
			return e
		} else if ok {
			return nil
		}
		return tx.Put("provider.retention.declarations", d.DeclarationID, b)
	})
}
