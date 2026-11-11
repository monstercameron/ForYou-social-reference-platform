package devnet

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"sort"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
)

const (
	// Release0Label is the fixed label for release key zero.
	Release0Label = "release/0"
	// Release1Label is the fixed label for release key one.
	Release1Label = "release/1"
	// Release2Label is the fixed label for release key two.
	Release2Label = "release/2"
	// Release3Label is the fixed label for release key three.
	Release3Label = "release/3"
	// Release4Label is the fixed label for release key four.
	Release4Label = "release/4"
	// Foundation0Label is the fixed label for the Foundation key.
	Foundation0Label = "foundation/0"
	// Monitor0Label is the fixed label for monitor zero.
	Monitor0Label = "monitor/0"
	// Monitor1Label is the fixed label for monitor one.
	Monitor1Label = "monitor/1"
	// Monitor2Label is the fixed label for monitor two.
	Monitor2Label = "monitor/2"
	// Monitor3Label is the fixed label for monitor three.
	Monitor3Label = "monitor/3"
	// Monitor4Label is the fixed label for monitor four.
	Monitor4Label = "monitor/4"
	// ProviderLabelPrefix prefixes each nonnegative provider index.
	ProviderLabelPrefix = "provider/"
	// Replica0Label is the fixed label for the replica key.
	Replica0Label = "replica/0"
)

var releaseLabels = [...]string{Release0Label, Release1Label, Release2Label, Release3Label, Release4Label}
var monitorLabels = [...]string{Monitor0Label, Monitor1Label, Monitor2Label, Monitor3Label, Monitor4Label}

func privateKey(label string) keys.PrivateKey {
	seed := sha256.Sum256([]byte("foryou-dev/v1/" + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

// FoundationKey returns the deterministic Foundation signing key.
func FoundationKey() keys.PrivateKey { return privateKey(Foundation0Label) }

// ReleaseKeys returns the five deterministic release signing keys in label order.
func ReleaseKeys() []keys.PrivateKey {
	return []keys.PrivateKey{privateKey(Release0Label), privateKey(Release1Label), privateKey(Release2Label), privateKey(Release3Label), privateKey(Release4Label)}
}

// MonitorKey returns the deterministic monitor signing key for n, or nil outside [0, 4].
func MonitorKey(n int) keys.PrivateKey {
	if n < 0 || n >= len(monitorLabels) {
		return nil
	}
	return privateKey(monitorLabels[n])
}

// ProviderKey returns the deterministic provider signing key for n.
func ProviderKey(n int) keys.PrivateKey {
	if n < 0 {
		return nil
	}
	return privateKey(fmt.Sprintf("%s%d", ProviderLabelPrefix, n))
}

// ReplicaKey returns the deterministic replica signing key.
func ReplicaKey() keys.PrivateKey { return privateKey(Replica0Label) }

// Recipient returns faux.DevAddress for an owner and supported network.
func Recipient(owner, network string) string { return faux.DevAddress(network, owner) }

func releaseIDs() []string {
	ids := make([]string, 0, len(releaseLabels))
	for _, label := range releaseLabels {
		ids = append(ids, keys.DIDOf(privateKey(label)))
	}
	sort.Strings(ids)
	return ids
}

func monitorIDs() []string {
	ids := make([]string, 0, len(monitorLabels))
	for _, label := range monitorLabels {
		ids = append(ids, keys.DIDOf(privateKey(label)))
	}
	sort.Strings(ids)
	return ids
}
