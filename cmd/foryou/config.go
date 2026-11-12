package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"foryou.local/foryou/internal/transport"
)

// roleOrder defines the canonical order in which roles start.
var roleOrder = []string{"faux", "foundation", "monitor", "provider", "replica", "client"}

var rolePorts = map[string]int{"client": 8443, "foundation": 8444, "provider": 8445, "replica": 8446, "monitor": 8447, "faux": 8448}

// defaultAddr returns a role's loopback listen address.
func defaultAddr(role string) string { return fmt.Sprintf("127.0.0.1:%d", rolePorts[role]) }

// roleConfig contains the settings passed to a role builder.
type roleConfig struct {
	// Role identifies the service role.
	Role string
	// Dev enables the local development network behavior.
	Dev bool
	// Addr is this role's listen address.
	Addr string
	// URL is this role's externally reachable base URL.
	URL string
	// Peers maps every role name to its base URL.
	Peers map[string]string
	// DataDir is this role's persistent data directory.
	DataDir string
	// Now supplies the service clock; nil means time.Now.
	Now func() time.Time
	// LoopInterval overrides the service background loop interval when nonzero.
	LoopInterval time.Duration
	// ClaimPeriod overrides the service claim period when nonzero.
	ClaimPeriod time.Duration
}

func loadRoleConfig(role string, getenv func(string) string) (roleConfig, transport.Config, error) {
	if _, ok := rolePorts[role]; !ok {
		return roleConfig{}, transport.Config{}, fmt.Errorf("invalid role %q", role)
	}
	devValue := getenv("FORYOU_DEV")
	if devValue != "" && devValue != "1" {
		return roleConfig{}, transport.Config{}, fmt.Errorf("FORYOU_DEV must be empty or 1, got %q", devValue)
	}
	dev := devValue == "1"
	cert, key := getenv("FORYOU_TLS_CERT"), getenv("FORYOU_TLS_KEY")
	if (cert == "") != (key == "") {
		return roleConfig{}, transport.Config{}, transport.ErrIncompleteTLS
	}
	base := getenv("FORYOU_DATA_DIR")
	if base == "" {
		base = filepath.Join(".local", "data")
	}
	peers := make(map[string]string, len(roleOrder))
	for _, name := range roleOrder {
		addr := getenv("FORYOU_" + strings.ToUpper(name) + "_ADDR")
		if addr == "" {
			addr = defaultAddr(name)
		}
		peerURL := getenv("FORYOU_" + strings.ToUpper(name) + "_URL")
		if peerURL == "" {
			scheme := "https"
			if dev {
				scheme = "http"
			}
			peerURL = scheme + "://" + addr
		}
		if _, err := url.ParseRequestURI(peerURL); err != nil {
			return roleConfig{}, transport.Config{}, fmt.Errorf("invalid FORYOU_%s_URL: %w", strings.ToUpper(name), err)
		}
		peers[name] = peerURL
	}
	addr := getenv("FORYOU_" + strings.ToUpper(role) + "_ADDR")
	if addr == "" {
		addr = defaultAddr(role)
	}
	baseURL := getenv("FORYOU_" + strings.ToUpper(role) + "_URL")
	if baseURL == "" {
		baseURL = peers[role]
	}
	config := roleConfig{Role: role, Dev: dev, Addr: addr, URL: baseURL, Peers: peers, DataDir: filepath.Join(base, role)}
	return config, transport.Config{Addr: addr, CertFile: cert, KeyFile: key, Dev: dev}, nil
}
