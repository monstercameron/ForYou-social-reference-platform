package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
)

const controlCheckpointGeneration uint64 = 1

type controlCheckpoint struct {
	// Operator names the provider DID.
	Operator string `json:"operator"`
	// Generation is the control-state generation.
	Generation uint64 `json:"generation"`
	// Sequence is the last acknowledged control sequence.
	Sequence uint64 `json:"sequence"`
	// Digest commits to the ordered control history.
	Digest string `json:"digest"`
	// Signature authenticates the other checkpoint fields.
	Signature string `json:"signature"`
}

type controlCheckpointUnsigned struct {
	// Operator names the provider DID.
	Operator string `json:"operator"`
	// Generation is the control-state generation.
	Generation uint64 `json:"generation"`
	// Sequence is the last acknowledged control sequence.
	Sequence uint64 `json:"sequence"`
	// Digest commits to the ordered control history.
	Digest string `json:"digest"`
}

func controlInitialDigest(operator string, generation uint64) ([32]byte, error) {
	encoded, err := canon.Encode(map[string]any{"generation": generation, "operator": operator})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func controlNextDigest(previous [32]byte, event controlEvent) ([32]byte, error) {
	encoded, err := canon.Encode(map[string]any{
		"event":    event,
		"previous": hex.EncodeToString(previous[:]),
		"sequence": event.Sequence,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (s *Service) controlCheckpointPath() string {
	return filepath.Join(s.DataDir(), "control-checkpoint.json")
}

func (s *Service) readControlCheckpoint() (*controlCheckpoint, error) {
	if s.DataDir() == "" {
		return nil, nil
	}
	encoded, err := os.ReadFile(s.controlCheckpointPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint controlCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	} else if err == nil {
		return nil, errors.New("provider: trailing checkpoint data")
	}
	return &checkpoint, nil
}

func (s *Service) writeControlCheckpoint(sequence uint64, digest [32]byte) error {
	unsigned := controlCheckpointUnsigned{
		Operator:   s.ProviderID(),
		Generation: controlCheckpointGeneration,
		Sequence:   sequence,
		Digest:     hex.EncodeToString(digest[:]),
	}
	signature, err := s.Sign("control-checkpoint", unsigned)
	if err != nil {
		return err
	}
	checkpoint := controlCheckpoint{Operator: unsigned.Operator, Generation: unsigned.Generation, Sequence: unsigned.Sequence, Digest: unsigned.Digest, Signature: signature}
	if s.DataDir() == "" {
		return nil
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.DataDir(), ".control-checkpoint-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if _, err = file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempPath, s.controlCheckpointPath()); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(s.DataDir())
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func controlVerifyCheckpoint(s *Service, checkpoint controlCheckpoint) error {
	if checkpoint.Operator != s.ProviderID() || checkpoint.Generation != controlCheckpointGeneration {
		return errors.New("provider: control checkpoint identity mismatch")
	}
	unsigned := controlCheckpointUnsigned{
		Operator:   checkpoint.Operator,
		Generation: checkpoint.Generation,
		Sequence:   checkpoint.Sequence,
		Digest:     checkpoint.Digest,
	}
	canonical, err := canon.Encode(unsigned)
	if err != nil {
		return err
	}
	if err = keys.Verify(checkpoint.Operator, "control-checkpoint", canonical, checkpoint.Signature); err != nil {
		return fmt.Errorf("provider: invalid control checkpoint signature: %w", err)
	}
	return nil
}

func controlReadEvents(s *Service) ([]controlEvent, error) {
	events := make([]controlEvent, 0)
	err := s.ControlStore().View(func(tx store.Tx) error {
		return tx.Scan("provider.controls.log", store.Range{}, func(key string, value []byte) (bool, error) {
			var event controlEvent
			decoder := json.NewDecoder(bytes.NewReader(value))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&event); err != nil {
				return false, err
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				if err == nil {
					return false, errors.New("provider: trailing control event data")
				}
				return false, err
			}
			if event.Sequence != uint64(len(events)+1) || key != fmt.Sprintf("%020d", event.Sequence) {
				return false, errors.New("provider: control log sequence mismatch")
			}
			if err := controlValidateEvent(event); err != nil {
				return false, err
			}
			events = append(events, event)
			return true, nil
		})
	})
	return events, err
}
