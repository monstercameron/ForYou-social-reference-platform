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
	"strings"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
)

const (
	controlCheckpointGeneration uint64 = 1
	intakeCheckpointGeneration  uint64 = 1
	controlCheckpointName              = "control-checkpoint.json"
	intakeCheckpointName               = "intake-checkpoint.json"
	controlInitializedName             = "control-initialized"
	intakeInitializedName              = "intake-initialized"
)

type signedCheckpoint struct {
	// Operator names the provider DID.
	Operator string `json:"operator"`
	// Generation is the state-domain generation.
	Generation uint64 `json:"generation"`
	// Sequence is the last acknowledged sequence in the state domain.
	Sequence uint64 `json:"sequence"`
	// Digest commits to the ordered history up to Sequence.
	Digest string `json:"digest"`
	// Signature authenticates the other checkpoint fields.
	Signature string `json:"signature"`
}

type signedCheckpointBody struct {
	// Operator names the provider DID.
	Operator string `json:"operator"`
	// Generation is the state-domain generation.
	Generation uint64 `json:"generation"`
	// Sequence is the last acknowledged sequence in the state domain.
	Sequence uint64 `json:"sequence"`
	// Digest commits to the ordered history up to Sequence.
	Digest string `json:"digest"`
}

// checkpointLocation validates ControlCheckpointDir and creates it. The directory must lie outside DataDir so that
// restoring DataDir from an older backup cannot also restore the checkpoints that detect the rollback.
func checkpointLocation(cfg *Config) error {
	if cfg.DataDir == "" {
		return nil
	}
	if cfg.ControlCheckpointDir == "" {
		if !cfg.Dev {
			return invalidField("ControlCheckpointDir", "is required when DataDir is set")
		}
		dataDir := filepath.Clean(cfg.DataDir)
		cfg.ControlCheckpointDir = filepath.Join(filepath.Dir(dataDir), filepath.Base(dataDir)+"-checkpoint")
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	checkpointDir, err := filepath.Abs(cfg.ControlCheckpointDir)
	if err != nil {
		return err
	}
	if pathWithin(checkpointDir, dataDir) || pathWithin(dataDir, checkpointDir) {
		return invalidField("ControlCheckpointDir", "must be outside DataDir")
	}
	return os.MkdirAll(checkpointDir, 0700)
}

func pathWithin(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

func (s *Service) checkpointFile(name string) string {
	return filepath.Join(s.cfg.ControlCheckpointDir, name)
}

func (s *Service) readCheckpoint(name string) (*signedCheckpoint, error) {
	if s.DataDir() == "" {
		return nil, nil
	}
	encoded, err := os.ReadFile(s.checkpointFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint signedCheckpoint
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

func (s *Service) writeCheckpoint(name, label string, generation, sequence uint64, digest [32]byte) error {
	if s.DataDir() == "" {
		return nil
	}
	body := signedCheckpointBody{Operator: s.ProviderID(), Generation: generation, Sequence: sequence, Digest: hex.EncodeToString(digest[:])}
	signature, err := s.Sign(label, body)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(signedCheckpoint{Operator: body.Operator, Generation: body.Generation, Sequence: body.Sequence, Digest: body.Digest, Signature: signature})
	if err != nil {
		return err
	}
	return writeIndependentFile(s.cfg.ControlCheckpointDir, name, encoded)
}

func (s *Service) verifyCheckpoint(label string, generation uint64, checkpoint signedCheckpoint) error {
	if checkpoint.Operator != s.ProviderID() || checkpoint.Generation != generation {
		return fmt.Errorf("provider: %s checkpoint identity mismatch", label)
	}
	canonical, err := canon.Encode(signedCheckpointBody{Operator: checkpoint.Operator, Generation: checkpoint.Generation, Sequence: checkpoint.Sequence, Digest: checkpoint.Digest})
	if err != nil {
		return err
	}
	if err = keys.Verify(checkpoint.Operator, label, canonical, checkpoint.Signature); err != nil {
		return fmt.Errorf("provider: invalid %s checkpoint signature: %w", label, err)
	}
	return nil
}

// initialized reports whether the first-start marker exists. The marker lives in the independent directory, so a
// restored DataDir cannot erase it.
func (s *Service) initialized(name string) (bool, error) {
	if s.DataDir() == "" {
		return false, nil
	}
	_, err := os.Stat(s.checkpointFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// markInitialized writes the first-start marker once. Recovery from a verified checkpoint calls it too, so a crash
// between the first checkpoint and the marker cannot leave the domain treated as fresh later.
func (s *Service) markInitialized(name string) error {
	initialized, err := s.initialized(name)
	if err != nil || initialized || s.DataDir() == "" {
		return err
	}
	return writeIndependentFile(s.cfg.ControlCheckpointDir, name, []byte(s.ProviderID()))
}

func (s *Service) readControlCheckpoint() (*signedCheckpoint, error) {
	return s.readCheckpoint(controlCheckpointName)
}

func (s *Service) writeControlCheckpoint(sequence uint64, digest [32]byte) error {
	return s.writeCheckpoint(controlCheckpointName, "control-checkpoint", controlCheckpointGeneration, sequence, digest)
}

func (s *Service) verifyControlCheckpoint(checkpoint signedCheckpoint) error {
	return s.verifyCheckpoint("control-checkpoint", controlCheckpointGeneration, checkpoint)
}

func writeIndependentFile(dir, name string, encoded []byte) error {
	file, err := os.CreateTemp(dir, ".checkpoint-*")
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
	if err = os.Rename(tempPath, filepath.Join(dir, name)); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
