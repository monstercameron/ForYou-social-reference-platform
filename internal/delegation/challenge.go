package delegation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
)

const (
	// HeaderChallenge carries the canonical service challenge.
	HeaderChallenge = "ForYou-Service-Challenge"
	// HeaderSignature carries the root's service-auth signature.
	HeaderSignature = "ForYou-Service-Signature"
	// ChallengeLifetime is the maximum lifetime of an issued challenge.
	ChallengeLifetime = 60 * time.Second
	// MaxChallengeRequestBytes bounds an issuance request before parsing.
	MaxChallengeRequestBytes = 4096
	// MaxResourceBytes bounds a canonical request path.
	MaxResourceBytes = 2048
)

// ChallengeRequest is the operation a root authorizes with a service challenge.
type ChallengeRequest struct {
	AuthorID string `json:"author_id"`
	Method   string `json:"method"`
	Resource string `json:"resource"`
	BodyHash string `json:"body_hash"`
}

// RouteShape describes a supported root-authenticated route.
type RouteShape struct {
	Method    string
	Pattern   string // A {name} matches exactly one nonempty segment.
	Protected bool
}

// DefaultRouteShapes is the root-authenticated route set in the API catalog.
var DefaultRouteShapes = []RouteShape{
	{Method: "POST", Pattern: "/v1/deposits"},
	{Method: "POST", Pattern: "/v1/deposits/observe"},
	{Method: "GET", Pattern: "/v1/balances/{network}"},
	{Method: "GET", Pattern: "/v1/account-history/{network}/{after}"},
	{Method: "POST", Pattern: "/v1/withdrawals"},
	{Method: "GET", Pattern: "/v1/admissions/{message_id}"},
	{Method: "POST", Pattern: "/v1/authorization-epochs", Protected: true},
	{Method: "POST", Pattern: "/v1/authorization-epochs/control", Protected: true},
	{Method: "POST", Pattern: "/v1/authorization-epochs/state", Protected: true},
	{Method: "POST", Pattern: "/v1/revocations", Protected: true},
}

// ParseChallengeRequest parses a closed, bounded JSON issuance request.
func ParseChallengeRequest(body []byte) (ChallengeRequest, error) {
	if len(body) > MaxChallengeRequestBytes || !utf8.Valid(body) {
		return ChallengeRequest{}, ErrMalformedRequest
	}
	value, err := canon.Parse(body, canon.Limits{MaxBytes: MaxChallengeRequestBytes, MaxDepth: 4})
	if err != nil {
		return ChallengeRequest{}, ErrMalformedRequest
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 4 {
		return ChallengeRequest{}, ErrMalformedRequest
	}
	for _, name := range []string{"author_id", "method", "resource", "body_hash"} {
		if _, ok := object[name]; !ok {
			return ChallengeRequest{}, ErrMalformedRequest
		}
	}
	request := ChallengeRequest{}
	encoded, err := json.Marshal(object)
	if err != nil || json.Unmarshal(encoded, &request) != nil || challengeValidateRequest(request) != nil {
		return ChallengeRequest{}, ErrMalformedRequest
	}
	return request, nil
}

// ValidResource validates the exact canonical ASCII path representation.
func ValidResource(resource string) error {
	if len(resource) == 0 || len(resource) > MaxResourceBytes || resource[0] != '/' || !challengeIsASCII(resource) || strings.ContainsAny(resource, "?#") || !strings.HasPrefix(resource, "/v1/") {
		return ErrMalformedRequest
	}
	segments := strings.Split(resource, "/")
	for i, segment := range segments {
		if i < 2 {
			continue
		}
		if segment == "" || segment == "." || segment == ".." {
			return ErrMalformedRequest
		}
		var decodedSegment bytes.Buffer
		for j := 0; j < len(segment); j++ {
			if segment[j] == '%' {
				if j+2 >= len(segment) || !challengeUpperHex(segment[j+1]) || !challengeUpperHex(segment[j+2]) {
					return ErrMalformedRequest
				}
				decoded, _ := hex.DecodeString(segment[j+1 : j+3])
				if challengeUnreserved(decoded[0]) {
					return ErrMalformedRequest
				}
				decodedSegment.WriteByte(decoded[0])
				j += 2
			} else if !challengePathCharacter(segment[j]) {
				return ErrMalformedRequest
			} else {
				decodedSegment.WriteByte(segment[j])
			}
		}
		if !utf8.Valid(decodedSegment.Bytes()) {
			return ErrMalformedRequest
		}
	}
	return nil
}

// BodyKind selects the body representation bound by a route.
type BodyKind int

const (
	BodyNone   BodyKind = iota // GET with an empty body.
	BodyJSON                   // application/json canonicalized with JCS.
	BodyOctets                 // application/octet-stream hashed as raw bytes.
)

// BodyHash hashes a route body according to its protocol representation.
func BodyHash(kind BodyKind, body []byte) (string, error) {
	var material []byte
	switch kind {
	case BodyNone:
		if len(body) != 0 {
			return "", ErrContentType
		}
	case BodyJSON:
		canonical, err := canon.Canonicalize(body, canon.MessageLimits)
		if err != nil {
			return "", ErrMalformedRequest
		}
		material = canonical
	case BodyOctets:
		material = body
	default:
		return "", ErrMalformedRequest
	}
	digest := sha256.Sum256(material)
	return hex.EncodeToString(digest[:]), nil
}

func challengeValidateRequest(request ChallengeRequest) error {
	if _, err := keys.ParseDID(request.AuthorID); err != nil {
		return err
	}
	if request.Method != "GET" && request.Method != "POST" && request.Method != "PUT" {
		return ErrMalformedRequest
	}
	if ValidResource(request.Resource) != nil || len(request.BodyHash) != 64 {
		return ErrMalformedRequest
	}
	decoded, err := hex.DecodeString(request.BodyHash)
	if err != nil || hex.EncodeToString(decoded) != request.BodyHash {
		return ErrMalformedRequest
	}
	if request.Method == "GET" && request.BodyHash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return ErrMalformedRequest
	}
	return nil
}

func challengeRouteMatch(method, resource string, routes []RouteShape) (RouteShape, bool) {
	for _, route := range routes {
		if method != route.Method {
			continue
		}
		want, got := strings.Split(route.Pattern, "/"), strings.Split(resource, "/")
		if len(want) != len(got) {
			continue
		}
		matched := true
		for i := range want {
			if strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}") {
				if got[i] == "" {
					matched = false
					break
				}
			} else if want[i] != got[i] {
				matched = false
				break
			}
		}
		if matched {
			return route, true
		}
	}
	return RouteShape{}, false
}

func challengeUpperHex(b byte) bool { return b >= '0' && b <= '9' || b >= 'A' && b <= 'F' }

func challengeUnreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b))
}

func challengePathCharacter(b byte) bool {
	return challengeUnreserved(b) || strings.ContainsRune("!$&'()*+,;=:@", rune(b))
}

func challengeIsASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}
