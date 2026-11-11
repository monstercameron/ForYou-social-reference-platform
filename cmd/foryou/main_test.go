package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaultAddresses(t *testing.T) {
	check := func(t *testing.T, role, want string) {
		t.Helper()
		if got := defaultAddr(role); got != want {
			t.Errorf("defaultAddr(%q) = %q; want %q", role, got, want)
		}
	}
	t.Run("client", func(t *testing.T) { check(t, "client", "127.0.0.1:8443") })
	t.Run("foundation", func(t *testing.T) { check(t, "foundation", "127.0.0.1:8444") })
	t.Run("provider", func(t *testing.T) { check(t, "provider", "127.0.0.1:8445") })
	t.Run("replica", func(t *testing.T) { check(t, "replica", "127.0.0.1:8446") })
	t.Run("monitor", func(t *testing.T) { check(t, "monitor", "127.0.0.1:8447") })
	t.Run("faux", func(t *testing.T) { check(t, "faux", "127.0.0.1:8448") })
}

func TestLoadRoleConfig(t *testing.T) {
	t.Run("provider address updates the peer URL", func(t *testing.T) {
		env := mapEnv(map[string]string{"FORYOU_PROVIDER_ADDR": "127.0.0.1:9000", "FORYOU_DEV": "1"})
		providerConfig, transportConfig, err := loadRoleConfig("provider", env)
		if err != nil {
			t.Fatal(err)
		}
		if providerConfig.Addr != "127.0.0.1:9000" || transportConfig.Addr != "127.0.0.1:9000" {
			t.Errorf("provider addresses = %q and %q; want 127.0.0.1:9000", providerConfig.Addr, transportConfig.Addr)
		}
		config, _, err := loadRoleConfig("client", env)
		if err != nil {
			t.Fatal(err)
		}
		if config.Peers["provider"] != "http://127.0.0.1:9000" {
			t.Errorf("provider peer URL = %q", config.Peers["provider"])
		}
	})
	t.Run("dev enabled", func(t *testing.T) {
		config, _, err := loadRoleConfig("client", mapEnv(map[string]string{"FORYOU_DEV": "1"}))
		if err != nil || !config.Dev {
			t.Errorf("Dev = %t, error = %v; want true without error", config.Dev, err)
		}
	})
	t.Run("dev unset", func(t *testing.T) {
		config, _, err := loadRoleConfig("client", mapEnv(nil))
		if err != nil || config.Dev {
			t.Errorf("Dev = %t, error = %v; want false without error", config.Dev, err)
		}
	})
	t.Run("invalid dev value names variable", func(t *testing.T) {
		_, _, err := loadRoleConfig("client", mapEnv(map[string]string{"FORYOU_DEV": "true"}))
		if err == nil || !strings.Contains(err.Error(), "FORYOU_DEV") {
			t.Errorf("error = %v; want FORYOU_DEV", err)
		}
	})
	t.Run("dev peer uses HTTP", func(t *testing.T) {
		config, _, err := loadRoleConfig("replica", mapEnv(map[string]string{"FORYOU_DEV": "1"}))
		if err != nil {
			t.Fatal(err)
		}
		if config.Peers["replica"] != "http://127.0.0.1:8446" {
			t.Errorf("replica peer URL = %q", config.Peers["replica"])
		}
	})
	t.Run("TLS peer uses HTTPS", func(t *testing.T) {
		config, _, err := loadRoleConfig("replica", mapEnv(map[string]string{"FORYOU_TLS_CERT": "cert", "FORYOU_TLS_KEY": "key"}))
		if err != nil {
			t.Fatal(err)
		}
		if config.Peers["replica"] != "https://127.0.0.1:8446" {
			t.Errorf("replica peer URL = %q", config.Peers["replica"])
		}
	})
	t.Run("dev peer uses HTTP with certificate", func(t *testing.T) {
		config, _, err := loadRoleConfig("replica", mapEnv(map[string]string{"FORYOU_DEV": "1", "FORYOU_TLS_CERT": "cert", "FORYOU_TLS_KEY": "key"}))
		if err != nil {
			t.Fatal(err)
		}
		if config.Peers["replica"] != "http://127.0.0.1:8446" {
			t.Errorf("replica peer URL = %q", config.Peers["replica"])
		}
	})
	t.Run("explicit peer URL wins", func(t *testing.T) {
		config, _, err := loadRoleConfig("replica", mapEnv(map[string]string{"FORYOU_DEV": "1", "FORYOU_TLS_CERT": "cert", "FORYOU_TLS_KEY": "key", "FORYOU_REPLICA_URL": "https://replica.example"}))
		if err != nil {
			t.Fatal(err)
		}
		if config.Peers["replica"] != "https://replica.example" {
			t.Errorf("replica peer URL = %q", config.Peers["replica"])
		}
	})
	t.Run("default role data directory", func(t *testing.T) {
		config, _, err := loadRoleConfig("monitor", mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(".local", "data", "monitor"); config.DataDir != want {
			t.Errorf("DataDir = %q; want %q", config.DataDir, want)
		}
	})
	t.Run("configured role data directory", func(t *testing.T) {
		config, _, err := loadRoleConfig("monitor", mapEnv(map[string]string{"FORYOU_DATA_DIR": "/tmp/x"}))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/tmp/x", "monitor"); config.DataDir != want {
			t.Errorf("DataDir = %q; want %q", config.DataDir, want)
		}
	})
}

func TestRunRejectsUnknownRole(t *testing.T) {
	err := run(context.Background(), []string{"-role", "nosuch"}, mapEnv(nil), nil)
	if err == nil {
		t.Fatal("run returned nil for unknown role")
	}
	if !strings.Contains(err.Error(), "faux, foundation, monitor, provider, replica, client") {
		t.Errorf("error does not list all six roles: %v", err)
	}
}

func TestRunServesAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, []string{"-role", "client"}, mapEnv(map[string]string{"FORYOU_DEV": "1", "FORYOU_CLIENT_ADDR": "127.0.0.1:0", "FORYOU_DATA_DIR": t.TempDir()}), func(role, url string) {
			if role != "client" {
				t.Errorf("onListen role = %q; want client", role)
			}
			listening <- url
		})
	}()
	var url string
	select {
	case url = <-listening:
	case err := <-result:
		t.Fatalf("run returned before listening: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("run did not start listening")
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("health status = %d; want 200", response.StatusCode)
	}
	cancel()
	if err := <-result; err != nil {
		t.Errorf("run after cancellation = %v; want nil", err)
	}
}

func TestRegisterRoleRejectsUnknownRole(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registerRole did not panic for unknown role")
		}
	}()
	registerRole("nosuch", func(roleConfig) (service, error) { return nil, nil })
}
