package governance

import (
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// CertificateID calculates the content identifier for a Foundation key certificate.
func CertificateID(c wire.FoundationKeyCertificate) (string, error) {
	return digestID("fyfc2:", "foundation-key-certificate-id", c, "certificate_id", "release_signatures")
}

// CheckCertificate validates a Foundation certificate and its release threshold signatures.
func CheckCertificate(c wire.FoundationKeyCertificate, set KeySet, genesis bool) error {
	if err := schemaCheck("FoundationKeyCertificate", c); err != nil {
		return err
	}
	wantID, err := digestID("fyfc2:", "foundation-key-certificate-id", c, "certificate_id", "release_signatures")
	if err != nil || c.CertificateID != wantID {
		return fmt.Errorf("%w: certificate ID", ErrInvalid)
	}
	issued, errIssued := wire.ParseTime(c.IssuedAt)
	from, errFrom := wire.ParseTime(c.ValidFrom)
	until, errUntil := wire.ParseTime(c.ValidUntil)
	if errIssued != nil || errFrom != nil || errUntil != nil || until.Sub(from) <= 0 || until.Sub(from) > 366*24*time.Hour || (!genesis && from.Sub(issued) < 30*24*time.Hour) {
		return fmt.Errorf("%w: certificate validity interval", ErrInvalid)
	}
	unsignedBytes, err := canon.EncodeWithout(c, "release_signatures")
	if err != nil {
		return fmt.Errorf("%w: certificate encoding: %v", ErrInvalid, err)
	}
	unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: certificate encoding: %v", ErrInvalid, err)
	}
	return VerifyThreshold("foundation-key-certificate", unsigned, c.ReleaseSignatures, set)
}
