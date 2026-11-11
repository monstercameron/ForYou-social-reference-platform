package rank

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSafetySecrets(t *testing.T) {
	t.Run("embedded BIP-39 word list", func(t *testing.T) {
		data, err := os.ReadFile("lexicon/bip39-english.txt")
		if err != nil {
			t.Fatal(err)
		}
		words := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(words) != 2048 || words[0] != "abandon" || words[len(words)-1] != "zoo" {
			t.Fatalf("BIP-39 list has %d words, beginning %q and ending %q", len(words), words[0], words[len(words)-1])
		}
	})
	check := func(t *testing.T, input string, want []SecretMatch) {
		t.Helper()
		if got := safetySecrets([]string{input}); !reflect.DeepEqual(got, want) {
			t.Errorf("safetySecrets() = %#v; want %#v", got, want)
		}
	}
	t.Run("link check token fixture", func(t *testing.T) {
		check(t, "- run: linkcheck --root docs\n  env:\n    LINKCHECK_TOKEN: fy_demo_4f9c2b7e1a6d4c0f9b3e8a5d7c1f2e6b\n", []SecretMatch{{Kind: "assigned_secret", Text: "fy_demo_4f9c2b7e1a6d4c0f9b3e8a5d7c1f2e6b"}})
	})
	t.Run("AWS credentials", func(t *testing.T) {
		check(t, "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\nAWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\nREGION=eu-west-1\n", []SecretMatch{{Kind: "assigned_secret", Text: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, {Kind: "aws_access_key_id", Text: "AKIAIOSFODNN7EXAMPLE"}})
	})
	t.Run("OpenSSH PEM", func(t *testing.T) {
		check(t, "-----BEGIN OPENSSH PRIVATE KEY-----\nZGVtby1rZXktbm90LXJlYWwtZGVtby1rZXktbm90LXJlYWw=\n-----END OPENSSH PRIVATE KEY-----\n", []SecretMatch{{Kind: "pem_private_key", Text: "-----BEGIN OPENSSH PRIVATE KEY-----"}})
	})
	t.Run("JSON assigned API key", func(t *testing.T) {
		check(t, `{"exchange":"demo","api_key":"fy_demo_9a1c5e7b3d2f4a6c8e0b1d3f5a7c9e2b","api_secret":"fy_demo_secret_c0ffee11c0ffee22c0ffee33c0ffee44"}`, []SecretMatch{{Kind: "assigned_secret", Text: "fy_demo_9a1c5e7b3d2f4a6c8e0b1d3f5a7c9e2b"}})
	})
	t.Run("AWS identifier only", func(t *testing.T) {
		check(t, "aws_access_key_id = AKIA2E0A8F3B244C9D71\n", []SecretMatch{{Kind: "aws_access_key_id", Text: "AKIA2E0A8F3B244C9D71"}})
	})
	t.Run("message ID and digest are not secrets", func(t *testing.T) {
		check(t, "Reply to fy1:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 and the media digest 2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae", nil)
	})
	t.Run("ordinary API key instructions", func(t *testing.T) {
		check(t, "Rotate your API key: the docs explain how to do it safely.", nil)
	})
	t.Run("ordinary secret guidance", func(t *testing.T) {
		check(t, "Keep the secret: never commit tokens or passwords to the repository.", nil)
	})
	t.Run("hex key with nearby cue", func(t *testing.T) {
		check(t, "my private key: 0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318", []SecretMatch{{Kind: "hex_private_key", Text: "0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"}})
	})
	t.Run("distant hex cue is not a match", func(t *testing.T) {
		check(t, "This line talks about a private key in general terms, then much later: 4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318", nil)
	})
	t.Run("twelve BIP-39 words", func(t *testing.T) {
		check(t, "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", []SecretMatch{{Kind: "recovery_phrase", Text: "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"}})
	})
	t.Run("thirteen BIP-39 words", func(t *testing.T) {
		check(t, "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about abandon", nil)
	})
	t.Run("ordinary phrase near miss", func(t *testing.T) { check(t, "the quick brown fox jumps over the lazy dog every single day", nil) })
	t.Run("zero entropy is ignored", func(t *testing.T) { check(t, "password = aaaaaaaaaaaaaaaaaaaa", nil) })
	t.Run("assigned password", func(t *testing.T) {
		check(t, "password: Tr0ub4dor3xK9mQ2vLpZ", []SecretMatch{{Kind: "assigned_secret", Text: "Tr0ub4dor3xK9mQ2vLpZ"}})
	})
	t.Run("GitHub token", func(t *testing.T) {
		check(t, "ghp_AAAAAAAAAAAAAAAAAAAAbbbbbbbbbbbbbbbb", []SecretMatch{{Kind: "github_token", Text: "ghp_AAAAAAAAAAAAAAAAAAAAbbbbbbbbbbbbbbbb"}})
	})
	t.Run("Slack token", func(t *testing.T) {
		check(t, "xoxb-1234567890-abcdefghij", []SecretMatch{{Kind: "slack_token", Text: "xoxb-1234567890-abcdefghij"}})
	})
	t.Run("Stripe live key", func(t *testing.T) {
		check(t, "sk_live_a1B2c3D4e5F6g7H8i9J0k1L2", []SecretMatch{{Kind: "stripe_live_key", Text: "sk_live_a1B2c3D4e5F6g7H8i9J0k1L2"}})
	})
	t.Run("Stripe test key near miss", func(t *testing.T) { check(t, "sk_test_a1B2c3D4e5F6g7H8i9J0k1L2", nil) })
	t.Run("Google API key", func(t *testing.T) {
		check(t, "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q", []SecretMatch{{Kind: "google_api_key", Text: "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"}})
	})
	t.Run("JWT", func(t *testing.T) {
		check(t, "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", []SecretMatch{{Kind: "jwt", Text: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}})
	})
	t.Run("empty result is nil", func(t *testing.T) {
		if got := safetySecrets([]string{"nothing"}); got != nil {
			t.Fatalf("got %#v; want nil", got)
		}
	})
}
