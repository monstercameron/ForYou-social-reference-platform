package content

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// MaxURLLength is the maximum length of a restricted external URL in bytes.
const MaxURLLength = 4096

// ErrInvalidURL identifies an external URL outside the restricted profile.
var ErrInvalidURL = errors.New("content: invalid external URL")

// ExternalURL is a URL that satisfies the restricted HTTPS profile.
type ExternalURL struct {
	Raw          string // The exact signed URL string.
	Host         string // The canonical host without IPv6 brackets.
	Port         int    // The effective port, defaulting to 443.
	ExplicitPort bool   // Whether the URL included a port.
}

// ParseExternalURL validates raw without changing any of its bytes.
func ParseExternalURL(raw string) (ExternalURL, error) {
	invalid := func(rule string) (ExternalURL, error) {
		return ExternalURL{}, fmt.Errorf("%w: %s", ErrInvalidURL, rule)
	}
	if len(raw) == 0 || len(raw) > MaxURLLength {
		return invalid("length must be 1–4096 bytes")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x21 || raw[i] > 0x7e {
			return invalid("URL must contain printable ASCII only")
		}
		if raw[i] == '\\' {
			return invalid("backslashes are forbidden")
		}
	}
	if !strings.HasPrefix(raw, "https://") {
		return invalid("scheme must be lowercase https")
	}
	remaining := raw[len("https://"):]
	authorityEnd := strings.IndexAny(remaining, "/?#")
	if authorityEnd < 0 {
		authorityEnd = len(remaining)
	}
	authority := remaining[:authorityEnd]
	if authority == "" || strings.ContainsAny(authority, "@%") {
		return invalid("authority must be nonempty and contain no userinfo or percent escapes")
	}
	host, port, explicitPort, err := validParseAuthority(authority)
	if err != nil {
		return invalid(err.Error())
	}
	if err := validCheckComponents(remaining[authorityEnd:]); err != nil {
		return invalid(err.Error())
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" || parsed.Hostname() != host {
		return invalid("URL parser disagrees with the restricted authority")
	}
	parsedPort := 443
	if parsed.Port() != "" {
		parsedPort, err = strconv.Atoi(parsed.Port())
		if err != nil {
			return invalid("URL parser disagrees with the port")
		}
	}
	if parsedPort != port {
		return invalid("URL parser disagrees with the effective port")
	}
	return ExternalURL{Raw: raw, Host: host, Port: port, ExplicitPort: explicitPort}, nil
}

// ValidateExternalURL reports whether raw satisfies the restricted HTTPS profile.
func ValidateExternalURL(raw string) error {
	_, err := ParseExternalURL(raw)
	return err
}

func validParseAuthority(authority string) (string, int, bool, error) {
	var host, portText string
	var explicitPort bool
	if authority[0] == '[' {
		closing := strings.IndexByte(authority, ']')
		if closing < 0 {
			return "", 0, false, errors.New("invalid bracketed IPv6 authority")
		}
		host = authority[1:closing]
		rest := authority[closing+1:]
		if rest != "" {
			if rest[0] != ':' || len(rest) == 1 {
				return "", 0, false, errors.New("invalid IPv6 port")
			}
			explicitPort = true
			portText = rest[1:]
		}
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Zone() != "" || address.String() != host {
			return "", 0, false, errors.New("IPv6 must be canonical compressed lowercase without a zone")
		}
	} else {
		if strings.Count(authority, ":") > 1 {
			return "", 0, false, errors.New("IPv6 addresses must be bracketed")
		}
		if colon := strings.IndexByte(authority, ':'); colon >= 0 {
			host = authority[:colon]
			portText = authority[colon+1:]
			explicitPort = true
		} else {
			host = authority
		}
		if host == "" {
			return "", 0, false, errors.New("host must be nonempty")
		}
		if validIsNumericHost(host) {
			address, err := netip.ParseAddr(host)
			if err != nil || !address.Is4() || address.String() != host {
				return "", 0, false, errors.New("IPv4 must be canonical dotted decimal")
			}
		} else if err := validCheckDNSHost(host); err != nil {
			return "", 0, false, err
		}
	}
	port := 443
	if explicitPort {
		if portText == "" || (len(portText) > 1 && portText[0] == '0') {
			return "", 0, false, errors.New("port must be canonical decimal from 1 to 65535")
		}
		for i := 0; i < len(portText); i++ {
			if portText[i] < '0' || portText[i] > '9' {
				return "", 0, false, errors.New("port must be canonical decimal from 1 to 65535")
			}
		}
		parsedPort, err := strconv.Atoi(portText)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", 0, false, errors.New("port must be canonical decimal from 1 to 65535")
		}
		port = parsedPort
	}
	return host, port, explicitPort, nil
}

func validIsNumericHost(host string) bool {
	for i := 0; i < len(host); i++ {
		if (host[i] < '0' || host[i] > '9') && host[i] != '.' {
			return false
		}
	}
	return true
}

func validCheckDNSHost(host string) error {
	if len(host) > 253 || host[len(host)-1] == '.' {
		return errors.New("DNS name exceeds 253 bytes or has a trailing dot")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("DNS labels must be 1–63 lowercase letters, digits or interior hyphens")
		}
		for i := 0; i < len(label); i++ {
			if !((label[i] >= 'a' && label[i] <= 'z') || (label[i] >= '0' && label[i] <= '9') || label[i] == '-') {
				return errors.New("DNS labels must be 1–63 lowercase letters, digits or interior hyphens")
			}
		}
		if strings.HasPrefix(label, "xn--") {
			unicode, err := idna.Registration.ToUnicode(label)
			if err != nil {
				return errors.New("invalid IDNA2008 A-label")
			}
			ascii, err := idna.Registration.ToASCII(unicode)
			if err != nil || ascii != label {
				return errors.New("invalid IDNA2008 A-label")
			}
		}
	}
	final := labels[len(labels)-1]
	if strings.HasPrefix(final, "0x") || !strings.ContainsAny(final, "abcdefghijklmnopqrstuvwxyz") {
		return errors.New("final DNS label must contain a letter and must not start with 0x")
	}
	return nil
}

func validCheckComponents(components string) error {
	path := components
	query := ""
	fragment := ""
	if hash := strings.IndexByte(path, '#'); hash >= 0 {
		fragment = path[hash+1:]
		path = path[:hash]
	}
	if question := strings.IndexByte(path, '?'); question >= 0 {
		query = path[question+1:]
		path = path[:question]
	}
	if path != "" && path[0] != '/' {
		return errors.New("path must be empty or start with /")
	}
	if err := validCheckComponent(path, false); err != nil {
		return errors.New("invalid path: " + err.Error())
	}
	if err := validCheckComponent(query, true); err != nil {
		return errors.New("invalid query: " + err.Error())
	}
	if err := validCheckComponent(fragment, true); err != nil {
		return errors.New("invalid fragment: " + err.Error())
	}
	return nil
}

func validCheckComponent(component string, allowQuestion bool) error {
	for i := 0; i < len(component); i++ {
		value := component[i]
		if value == '%' {
			if i+2 >= len(component) || !validIsHex(component[i+1]) || !validIsHex(component[i+2]) {
				return errors.New("malformed percent escape")
			}
			escaped := validHexValue(component[i+1])<<4 | validHexValue(component[i+2])
			if escaped < 0x20 || escaped == 0x7f || escaped == '\\' {
				return errors.New("percent escape encodes a control or backslash")
			}
			i += 2
			continue
		}
		if validIsUnreservedOrSubDelim(value) || value == ':' || value == '@' || value == '/' || (allowQuestion && value == '?') {
			continue
		}
		return errors.New("character is outside the RFC 3986 component profile")
	}
	return nil
}

func validIsUnreservedOrSubDelim(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || strings.ContainsRune("-._~!$&'()*+,;=", rune(value))
}

func validIsHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func validHexValue(value byte) byte {
	if value >= '0' && value <= '9' {
		return value - '0'
	}
	if value >= 'a' && value <= 'f' {
		return value - 'a' + 10
	}
	return value - 'A' + 10
}
