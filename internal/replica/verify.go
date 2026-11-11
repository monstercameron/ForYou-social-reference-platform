package replica

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

type syRecordFailure struct {
	sequence uint64
	err      error
}

func syVerifyPage(result sdk.ChangesResult, providerID string, now time.Time) ([]Copy, []syRecordFailure, error) {
	var rawPage struct {
		Items []struct {
			Message json.RawMessage `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(result.Raw, &rawPage); err != nil || len(rawPage.Items) != len(result.Page.Items) {
		return nil, nil, errors.New("changes page raw items do not match decoded items")
	}
	copies := make([]Copy, 0, len(result.Page.Items))
	var failures []syRecordFailure
	for index, item := range result.Page.Items {
		sequence := uint64(item.Sequence)
		copied, err := syVerifyRecord(rawPage.Items[index].Message, providerID, sequence, now)
		if err != nil {
			failures = append(failures, syRecordFailure{sequence: sequence, err: err})
			continue
		}
		copies = append(copies, copied)
	}
	return copies, failures, nil
}

func syVerifyRecord(raw []byte, providerID string, sequence uint64, now time.Time) (Copy, error) {
	if len(raw) > message.MaxMessageBytes {
		return Copy{}, errors.New("message exceeds size limit")
	}
	var post wire.PostMessage
	if err := wire.Decode(raw, "PostMessage", &post); err != nil {
		return Copy{}, fmt.Errorf("post message decode: %w", err)
	}
	canonical, err := canon.Encode(post)
	if err != nil {
		return Copy{}, fmt.Errorf("canonical message encoding: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return Copy{}, errors.New("message bytes are not canonical")
	}
	if post.Version.Major != 2 || post.Version.Minor != 0 {
		return Copy{}, errors.New("unsupported message version")
	}
	if err := message.Verify(post, message.VerifyOptions{Now: now, Delegation: delegation.CheckIntentDelegation}); err != nil {
		return Copy{}, fmt.Errorf("message verification: %w", err)
	}
	if post.SubmissionReceipt.ProviderID != providerID {
		return Copy{}, errors.New("receipt provider does not match pinned provider")
	}
	if err := content.ValidatePostContent(post.Payload); err != nil {
		return Copy{}, fmt.Errorf("post content validation: %w", err)
	}
	return Copy{MessageID: post.MessageID, Bytes: append([]byte(nil), raw...), SourceSequence: sequence}, nil
}
