package sdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"foryou.local/foryou/internal/discovery"
)

// Error is a validated public HTTP error returned by one service endpoint.
type Error struct {
	// HTTPStatus is the response status code.
	HTTPStatus int
	// Status is the validated public error name.
	Status string
	// RetryAfter is the required delay returned by rate limited and unavailable responses.
	RetryAfter time.Duration
	// Endpoint is the request path that returned the response.
	Endpoint string
}

// Error returns the endpoint, HTTP status and validated public status name.
func (e *Error) Error() string {
	return fmt.Sprintf("sdk: %s returned HTTP %d (%s)", e.Endpoint, e.HTTPStatus, e.Status)
}

// Signal is the client-visible category of a transport or public response error.
type Signal int

const (
	// SignalOther is an uncategorized local or transport error.
	SignalOther Signal = iota
	// SignalTimeout reports a request deadline.
	SignalTimeout
	// SignalNotFound reports provider-local nondisclosure or absence.
	SignalNotFound
	// SignalRetentionExpired reports provider-local retention expiry.
	SignalRetentionExpired
	// SignalRateLimited reports a disclosed request limit.
	SignalRateLimited
	// SignalUnavailable reports service or capacity unavailability.
	SignalUnavailable
	// SignalPolicyRestricted reports a policy restriction.
	SignalPolicyRestricted
	// SignalLegalRestricted reports a legal restriction.
	SignalLegalRestricted
	// SignalRestricted reports a restriction without a stated basis.
	SignalRestricted
	// SignalInvalidQuery reports a malformed query.
	SignalInvalidQuery
	// SignalInvalidCursor reports an invalid pagination cursor.
	SignalInvalidCursor
	// SignalProtocol reports a malformed or unexpected response.
	SignalProtocol
)

// String returns the stable lowercase name of s.
func (s Signal) String() string {
	switch s {
	case SignalTimeout:
		return "timeout"
	case SignalNotFound:
		return "not_found"
	case SignalRetentionExpired:
		return "retention_expired"
	case SignalRateLimited:
		return "rate_limited"
	case SignalUnavailable:
		return "unavailable"
	case SignalPolicyRestricted:
		return "policy_restricted"
	case SignalLegalRestricted:
		return "legal_restricted"
	case SignalRestricted:
		return "restricted"
	case SignalInvalidQuery:
		return "invalid_query"
	case SignalInvalidCursor:
		return "invalid_cursor"
	case SignalProtocol:
		return "protocol"
	default:
		return "other"
	}
}

// Classify distinguishes availability and restriction signals. A 403, 404, or 410 describes one provider only and never global absence, deletion, or illegality; restricted carries no legal basis.
func Classify(err error) Signal {
	if err == nil {
		return SignalOther
	}
	var e *Error
	if errors.As(err, &e) {
		switch e.Status {
		case "not_found":
			return SignalNotFound
		case "retention_expired":
			return SignalRetentionExpired
		case "rate_limited":
			return SignalRateLimited
		case "unavailable":
			return SignalUnavailable
		case "policy_restricted":
			return SignalPolicyRestricted
		case "legal_restricted":
			return SignalLegalRestricted
		case "restricted":
			return SignalRestricted
		case "invalid_query":
			return SignalInvalidQuery
		case "invalid_cursor":
			return SignalInvalidCursor
		}
	}
	var queryError *discovery.Error
	if errors.As(err, &queryError) {
		if queryError.Status == discovery.ErrInvalidCursor.Status {
			return SignalInvalidCursor
		}
		if queryError.Status == discovery.ErrInvalidQuery.Status {
			return SignalInvalidQuery
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SignalTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return SignalTimeout
	}
	if errors.Is(err, rdErrProtocol) {
		return SignalProtocol
	}
	return SignalOther
}

// IsAmbiguous reports whether a state-changing request may have taken effect despite its error.
func IsAmbiguous(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.HTTPStatus >= 500 && e.HTTPStatus <= 599
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}

var rdErrProtocol = errors.New("sdk: protocol error")

func rdProtocol(message string) error { return fmt.Errorf("%w: %s", rdErrProtocol, message) }
func rdProtocolHTTP(status int, message string) error {
	return fmt.Errorf("%w: %w: %s", rdErrProtocol, &Error{HTTPStatus: status}, message)
}
func rdStatus(err error, status string) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}
func rdParseTimestamp(value string) (time.Time, error) {
	const layout = "2006-01-02T15:04:05Z"
	parsed, err := time.Parse(layout, value)
	if err != nil || parsed.Format(layout) != value {
		return time.Time{}, errors.New("sdk: invalid canonical timestamp")
	}
	return parsed, nil
}
