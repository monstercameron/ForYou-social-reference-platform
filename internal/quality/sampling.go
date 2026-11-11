package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
)

// SeedCommitment returns the commitment digest for a canonical 32-byte seed.
func SeedCommitment(seed string) (string, error) {
	if _, err := keys.DecodeB64URL(seed, 32); err != nil {
		return "", fmt.Errorf("%w: %v", ErrSeed, err)
	}
	canonical, err := canon.Encode(struct {
		SampleSeed string `json:"sample_seed"`
	}{seed})
	if err != nil {
		return "", err
	}
	input, err := keys.SigningInput("quality-sample-seed", canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:]), nil
}

// FrameDigest validates and hashes the canonical sampling frame.
func FrameDigest(frame []string) (string, error) {
	if err := validateFrame(frame); err != nil {
		return "", err
	}
	canonical, err := canon.Encode(frame)
	if err != nil {
		return "", err
	}
	return digestHex(canonical), nil
}

// Select returns the first n message IDs ordered by their deterministic sample ranks.
func Select(seed string, frame []string, n int) ([]string, error) {
	if _, err := keys.DecodeB64URL(seed, 32); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSeed, err)
	}
	if err := validateFrame(frame); err != nil {
		return nil, err
	}
	if n < 0 || n > len(frame) {
		return nil, fmt.Errorf("%w: sample size outside frame", ErrFrame)
	}
	type ranked struct {
		id   string
		rank [32]byte
	}
	ranks := make([]ranked, len(frame))
	for i, id := range frame {
		payload, err := canon.Encode(struct {
			SampleSeed string `json:"sample_seed"`
			MessageID  string `json:"message_id"`
		}{seed, id})
		if err != nil {
			return nil, err
		}
		input, err := keys.SigningInput("quality-sample-rank", payload)
		if err != nil {
			return nil, err
		}
		ranks[i] = ranked{id: id, rank: sha256.Sum256(input)}
	}
	sort.Slice(ranks, func(i, j int) bool {
		comparison := strings.Compare(string(ranks[i].rank[:]), string(ranks[j].rank[:]))
		if comparison == 0 {
			return ranks[i].id < ranks[j].id
		}
		return comparison < 0
	})
	selected := make([]string, n)
	for i := range selected {
		selected[i] = ranks[i].id
	}
	return selected, nil
}

func validateFrame(frame []string) error {
	if len(frame) > 10000 {
		return fmt.Errorf("%w: frame exceeds 10000 IDs", ErrFrame)
	}
	for i, id := range frame {
		if len(id) != 68 || !strings.HasPrefix(id, "fy1:") {
			return fmt.Errorf("%w: invalid message ID", ErrFrame)
		}
		for _, digit := range id[4:] {
			if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f') {
				return fmt.Errorf("%w: invalid message ID", ErrFrame)
			}
		}
		if i > 0 && frame[i-1] >= id {
			return fmt.Errorf("%w: frame must be unique and ASCII sorted", ErrFrame)
		}
	}
	return nil
}
