package content

import (
	"errors"
	"strings"
	"testing"
)

func TestParseExternalURL(t *testing.T) {
	check := func(t *testing.T, input, wantRaw, wantHost string, wantPort int, wantExplicit bool, wantErr bool) {
		t.Helper()
		got, err := ParseExternalURL(input)
		if wantErr {
			if !errors.Is(err, ErrInvalidURL) {
				t.Fatalf("ParseExternalURL(%q) error = %v; want ErrInvalidURL", input, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("ParseExternalURL(%q) error = %v", input, err)
		}
		if got.Raw != wantRaw || got.Host != wantHost || got.Port != wantPort || got.ExplicitPort != wantExplicit {
			t.Fatalf("ParseExternalURL(%q) = %+v; want Raw=%q Host=%q Port=%d ExplicitPort=%t", input, got, wantRaw, wantHost, wantPort, wantExplicit)
		}
	}
	t.Run("media_url_with_query_and_fragment", func(t *testing.T) {
		check(t, "https://media.example/a?x=1#t=2", "https://media.example/a?x=1#t=2", "media.example", 443, false, false)
	})
	t.Run("maximum_explicit_port", func(t *testing.T) {
		check(t, "https://media.example:65535/a", "https://media.example:65535/a", "media.example", 65535, true, false)
	})
	t.Run("canonical_ipv4", func(t *testing.T) {
		check(t, "https://192.0.2.1/a", "https://192.0.2.1/a", "192.0.2.1", 443, false, false)
	})
	t.Run("canonical_ipv6_with_explicit_default_port", func(t *testing.T) {
		check(t, "https://[2001:db8::1]:443/a", "https://[2001:db8::1]:443/a", "2001:db8::1", 443, true, false)
	})
	t.Run("uppercase_ipv6_is_noncanonical", func(t *testing.T) {
		check(t, "https://[2001:DB8::1]/a", "", "", 0, false, true)
	})
	t.Run("uncompressed_ipv6_is_noncanonical", func(t *testing.T) {
		check(t, "https://[2001:db8:0:0:0:0:0:1]/a", "", "", 0, false, true)
	})
	t.Run("valid_idna_alabel", func(t *testing.T) {
		check(t, "https://xn--bcher-kva.example/a%20b", "https://xn--bcher-kva.example/a%20b", "xn--bcher-kva.example", 443, false, false)
	})
	t.Run("private_address_is_syntactically_valid", func(t *testing.T) {
		check(t, "https://127.0.0.1/a", "https://127.0.0.1/a", "127.0.0.1", 443, false, false)
	})
	t.Run("preserves_raw_escaped_slash", func(t *testing.T) {
		check(t, "https://media.example/a%2Fb", "https://media.example/a%2Fb", "media.example", 443, false, false)
	})
	t.Run("maximum_length", func(t *testing.T) {
		input := "https://example.com/" + strings.Repeat("a", 4076)
		check(t, input, input, "example.com", 443, false, false)
	})
	t.Run("port_above_range", func(t *testing.T) { check(t, "https://example.com:999999/a", "", "", 0, false, true) })
	t.Run("port_above_maximum", func(t *testing.T) { check(t, "https://example.com:65536/a", "", "", 0, false, true) })
	t.Run("nonnumeric_port", func(t *testing.T) { check(t, "https://example.com:abc/a", "", "", 0, false, true) })
	t.Run("zero_port", func(t *testing.T) { check(t, "https://example.com:0/a", "", "", 0, false, true) })
	t.Run("empty_port", func(t *testing.T) { check(t, "https://example.com:/a", "", "", 0, false, true) })
	t.Run("leading_zero_port", func(t *testing.T) { check(t, "https://example.com:0443/a", "", "", 0, false, true) })
	t.Run("short_numeric_ipv4", func(t *testing.T) { check(t, "https://127.1/a", "", "", 0, false, true) })
	t.Run("bracketed_ipv4", func(t *testing.T) { check(t, "https://[192.0.2.1]/a", "", "", 0, false, true) })
	t.Run("unbracketed_ipv6", func(t *testing.T) { check(t, "https://2001:db8::1/a", "", "", 0, false, true) })
	t.Run("integer_ipv4", func(t *testing.T) { check(t, "https://2130706433/a", "", "", 0, false, true) })
	t.Run("leading_zero_ipv4", func(t *testing.T) { check(t, "https://0177.0.0.1/a", "", "", 0, false, true) })
	t.Run("hexadecimal_numeric_host", func(t *testing.T) { check(t, "https://0x7f000001/a", "", "", 0, false, true) })
	t.Run("trailing_dns_dot", func(t *testing.T) { check(t, "https://example.com./a", "", "", 0, false, true) })
	t.Run("final_dns_label_without_letter", func(t *testing.T) { check(t, "https://example.123/a", "", "", 0, false, true) })
	t.Run("leading_hyphen_dns_label", func(t *testing.T) { check(t, "https://-example.com/a", "", "", 0, false, true) })
	t.Run("overlength_dns_label", func(t *testing.T) {
		check(t, "https://"+strings.Repeat("a", 64)+".example/a", "", "", 0, false, true)
	})
	t.Run("uppercase_dns", func(t *testing.T) { check(t, "https://EXAMPLE.com/a", "", "", 0, false, true) })
	t.Run("userinfo", func(t *testing.T) { check(t, "https://user:pass@example.com/a", "", "", 0, false, true) })
	t.Run("escaped_authority", func(t *testing.T) { check(t, "https://%65xample.com/a", "", "", 0, false, true) })
	t.Run("ipv6_zone", func(t *testing.T) { check(t, "https://[::1%25zone]/a", "", "", 0, false, true) })
	t.Run("malformed_escape", func(t *testing.T) { check(t, "https://example.com/%zz", "", "", 0, false, true) })
	t.Run("escaped_control", func(t *testing.T) { check(t, "https://example.com/%0d", "", "", 0, false, true) })
	t.Run("escaped_backslash", func(t *testing.T) { check(t, "https://example.com/%5C", "", "", 0, false, true) })
	t.Run("literal_backslash", func(t *testing.T) { check(t, "https://example.com/a\\b", "", "", 0, false, true) })
	t.Run("newline", func(t *testing.T) { check(t, "https://example.com/a\n", "", "", 0, false, true) })
	t.Run("unicode_path", func(t *testing.T) { check(t, "https://example.com/é", "", "", 0, false, true) })
	t.Run("invalid_alabel", func(t *testing.T) { check(t, "https://xn--a.example/a", "", "", 0, false, true) })
	t.Run("angle_brackets", func(t *testing.T) { check(t, "https://example.com/<svg>", "", "", 0, false, true) })
	t.Run("http_scheme", func(t *testing.T) { check(t, "http://example.com/x", "", "", 0, false, true) })
	t.Run("uppercase_scheme", func(t *testing.T) { check(t, "HTTPS://example.com/x", "", "", 0, false, true) })
	t.Run("unicode_hostname", func(t *testing.T) { check(t, "https://bücher.example/", "", "", 0, false, true) })
	t.Run("overlength", func(t *testing.T) {
		input := "https://example.com/" + strings.Repeat("a", 4077)
		check(t, input, "", "", 0, false, true)
	})
}

func TestValidateExternalURL(t *testing.T) {
	if err := ValidateExternalURL("https://example.com/path"); err != nil {
		t.Fatalf("ValidateExternalURL() error = %v", err)
	}
	if err := ValidateExternalURL("https://example.com/%zz"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("ValidateExternalURL() error = %v; want ErrInvalidURL", err)
	}
}
