package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"foryou.local/foryou/internal/store"
)

type controlEvent struct {
	// Action is restrict, lift or remove.
	Action string `json:"action"`
	// At is the provider control commit time.
	At string `json:"at"`
	// MessageID is the affected signed record identifier.
	MessageID string `json:"message_id"`
	// Sequence is the contiguous provider control ordering.
	Sequence uint64 `json:"sequence"`
	// Status is the public status after this event.
	Status Restriction `json:"status"`
}

type controlRecord struct {
	// Status is the status reported for this record.
	Status Restriction `json:"status"`
	// Removed keeps legal removal irreversible.
	Removed bool `json:"removed"`
	// Sequence is the latest event affecting this record.
	Sequence uint64 `json:"sequence"`
}

// controlState stores this provider's local operator decisions. Shared deny-list or hash-list signals are not ingested,
// and control state is never published as a list of restricted identifiers.
type controlState struct {
	mu       sync.RWMutex
	sequence uint64
	digest   [32]byte
	records  map[string]controlRecord
}

type controlFailureState struct {
	err error
}

var controlMessageIDPattern = regexp.MustCompile(`^fy1:[0-9a-f]{64}$`)

func (c *controlFailureState) Restriction(string) (Restriction, error) {
	return Unrestricted, c.err
}

func (c *controlFailureState) Ready() error { return c.err }

func (c *controlState) Restriction(messageID string) (Restriction, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if record, ok := c.records[messageID]; ok {
		return record.Status, nil
	}
	return Unrestricted, nil
}

func (c *controlState) Ready() error { return nil }

func controlStatusValid(status Restriction) bool {
	return status == PolicyRestricted || status == LegalRestricted || status == Restricted || status == Concealed
}

func controlValidateEvent(event controlEvent) error {
	if !controlMessageIDPattern.MatchString(event.MessageID) || event.Sequence == 0 || event.At == "" {
		return errors.New("provider: invalid control event")
	}
	parsedAt, err := time.Parse(time.RFC3339, event.At)
	if err != nil || parsedAt.UTC().Format("2006-01-02T15:04:05Z") != event.At {
		return errors.New("provider: invalid control event time")
	}
	switch event.Action {
	case "restrict", "remove":
		if !controlStatusValid(event.Status) {
			return errors.New("provider: invalid control status")
		}
	case "lift":
		if event.Status != Unrestricted {
			return errors.New("provider: invalid lift status")
		}
	default:
		return errors.New("provider: invalid control action")
	}
	return nil
}

func init() {
	routeGroups = append(routeGroups, controlRegisterRoutes)
}

func controlRegisterRoutes(s *Service, mux *http.ServeMux) {
	controls, err := controlRecover(s)
	if err != nil {
		log.Printf("provider controls recovery: %v", err)
		s.SetControls(&controlFailureState{err: err})
	} else {
		s.SetControls(controls)
	}
	if s.Dev() {
		controlRegisterDevRoute(s, controls, mux)
	}
}

func controlRecover(s *Service) (*controlState, error) {
	events, err := controlReadEvents(s)
	if err != nil {
		return nil, err
	}
	checkpoint, err := s.readControlCheckpoint()
	if err != nil {
		return nil, err
	}
	if len(events) == 0 && checkpoint == nil {
		initial, err := controlInitialDigest(s.ProviderID(), controlCheckpointGeneration)
		if err != nil {
			return nil, err
		}
		empty := make(map[string]controlRecord)
		if err = controlPersistCurrent(s, empty); err != nil {
			return nil, err
		}
		return &controlState{digest: initial, records: empty}, nil
	}
	if checkpoint == nil {
		return nil, errors.New("provider: control checkpoint missing")
	}
	if err = controlVerifyCheckpoint(s, *checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.Sequence > uint64(len(events)) {
		return nil, errors.New("provider: control log shorter than checkpoint")
	}
	digest, err := controlInitialDigest(s.ProviderID(), controlCheckpointGeneration)
	if err != nil {
		return nil, err
	}
	if checkpoint.Sequence == 0 && fmt.Sprintf("%x", digest) != checkpoint.Digest {
		return nil, errors.New("provider: control checkpoint digest mismatch")
	}
	state := &controlState{records: make(map[string]controlRecord)}
	for _, event := range events {
		digest, err = controlNextDigest(digest, event)
		if err != nil {
			return nil, err
		}
		state.records[event.MessageID] = controlNext(state.records[event.MessageID], event)
		if event.Sequence == checkpoint.Sequence && fmt.Sprintf("%x", digest) != checkpoint.Digest {
			return nil, errors.New("provider: control checkpoint digest mismatch")
		}
	}
	state.sequence = uint64(len(events))
	state.digest = digest
	if err = controlPersistCurrent(s, state.records); err != nil {
		return nil, err
	}
	if err = controlReconcile(s, state.records); err != nil {
		return nil, err
	}
	if state.sequence > checkpoint.Sequence {
		if err = s.writeControlCheckpoint(state.sequence, state.digest); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func controlNext(current controlRecord, event controlEvent) controlRecord {
	current.Sequence = event.Sequence
	switch event.Action {
	case "restrict":
		current.Status = event.Status
	case "lift":
		if !current.Removed {
			current.Status = Unrestricted
		}
	case "remove":
		current.Status = event.Status
		current.Removed = true
	}
	return current
}

func controlPersistCurrent(s *Service, records map[string]controlRecord) error {
	return s.ControlStore().Update(func(tx store.Tx) error {
		if err := tx.Scan("provider.controls.current", store.Range{}, func(key string, _ []byte) (bool, error) {
			return true, tx.Delete("provider.controls.current", key)
		}); err != nil {
			return err
		}
		for id, record := range records {
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err = tx.Put("provider.controls.current", id, encoded); err != nil {
				return err
			}
		}
		return nil
	})
}

func controlReconcile(s *Service, records map[string]controlRecord) error {
	for id, record := range records {
		if record.Removed {
			if err := s.Records().Remove(id); err != nil {
				return err
			}
			s.MarkHidden(id)
		} else if record.Status != Unrestricted {
			s.MarkHidden(id)
		}
	}
	return nil
}

func controlApply(s *Service, controls *controlState, action, messageID string, status Restriction) (uint64, error) {
	validatedStatus, err := controlValidateRequest(action, messageID, string(status))
	if err != nil {
		return 0, err
	}
	status = validatedStatus
	controls.mu.Lock()
	defer controls.mu.Unlock()
	var event controlEvent
	var nextRecord controlRecord
	err = s.ControlStore().Update(func(tx store.Tx) error {
		sequence, err := tx.NextSequence("provider.controls")
		if err != nil {
			return err
		}
		event = controlEvent{Action: action, At: s.Now().Format("2006-01-02T15:04:05Z"), MessageID: messageID, Sequence: sequence, Status: status}
		if err = controlValidateEvent(event); err != nil {
			return err
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err = tx.Put("provider.controls.log", fmt.Sprintf("%020d", sequence), encoded); err != nil {
			return err
		}
		nextRecord = controlNext(controls.records[messageID], event)
		currentBytes, err := json.Marshal(nextRecord)
		if err != nil {
			return err
		}
		return tx.Put("provider.controls.current", messageID, currentBytes)
	})
	if err != nil {
		return 0, err
	}
	nextDigest, err := controlNextDigest(controls.digest, event)
	if err != nil {
		return 0, err
	}
	controls.records[messageID] = nextRecord
	controls.sequence = event.Sequence
	controls.digest = nextDigest
	if action == "remove" {
		if err = s.Records().Remove(messageID); err != nil {
			return 0, err
		}
	}
	if action == "restrict" || action == "remove" {
		s.MarkHidden(messageID)
	}
	if err = s.writeControlCheckpoint(controls.sequence, controls.digest); err != nil {
		return 0, err
	}
	return event.Sequence, nil
}
