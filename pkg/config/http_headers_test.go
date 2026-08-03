package config

import (
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeExtraHeaders(t *testing.T) {
	t.Parallel()

	t.Run("canonicalizes and collapses equivalent names", func(t *testing.T) {
		t.Parallel()

		headers, err := NormalizeExtraHeaders("test headers", map[string]string{
			"x-proxy-auth": "same",
			"X-Proxy-Auth": "same",
		})
		if err != nil {
			t.Fatalf("NormalizeExtraHeaders returned error: %v", err)
		}
		if len(headers) != 1 || headers["X-Proxy-Auth"] != "same" {
			t.Fatalf("expected one canonical header, got %#v", headers)
		}
	})

	t.Run("accepts HTTP token names and wire-safe values", func(t *testing.T) {
		t.Parallel()

		name := "!#$%&'*+-.^_`|~AZaz09"
		value := "visible\ttext\xff"
		headers, err := NormalizeExtraHeaders("test headers", map[string]string{name: value})
		if err != nil {
			t.Fatalf("NormalizeExtraHeaders returned error: %v", err)
		}
		canonicalName := http.CanonicalHeaderKey(name)
		if headers[canonicalName] != value {
			t.Fatalf("normalized headers = %#v, want %q: %q", headers, canonicalName, value)
		}
	})

	for _, tc := range []struct {
		name    string
		headers map[string]string
		wantErr string
	}{
		{
			name: "conflicting values",
			headers: map[string]string{
				"X-Proxy-Auth": "first",
				"x-proxy-auth": "second",
			},
			wantErr: `conflicting values for case-insensitive HTTP header "X-Proxy-Auth"`,
		},
		{name: "empty name", headers: map[string]string{"": "value"}, wantErr: "invalid HTTP header name"},
		{name: "space in name", headers: map[string]string{"Bad Header": "value"}, wantErr: "invalid HTTP header name"},
		{name: "colon in name", headers: map[string]string{"Bad:Header": "value"}, wantErr: "invalid HTTP header name"},
		{name: "unicode name", headers: map[string]string{"X-💡": "value"}, wantErr: "invalid HTTP header name"},
		{name: "NUL value", headers: map[string]string{"X-Test": "bad\x00value"}, wantErr: `invalid HTTP header value for "X-Test"`},
		{name: "vertical tab value", headers: map[string]string{"X-Test": "bad\vvalue"}, wantErr: `invalid HTTP header value for "X-Test"`},
		{name: "DEL value", headers: map[string]string{"X-Test": "bad\x7fvalue"}, wantErr: `invalid HTTP header value for "X-Test"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := NormalizeExtraHeaders("test headers", tc.headers)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestEncodedExtraHeaderMapRoundTrip(t *testing.T) {
	t.Parallel()

	raw := map[string]string{
		"x-composite": "first; second, third\xff",
		"X-From-Env":  "env:HEADER_SECRET",
	}
	encoded, err := encodeExtraHeaderMap("test headers", raw)
	if err != nil {
		t.Fatalf("encodeExtraHeaderMap returned error: %v", err)
	}
	if !strings.HasPrefix(encoded, encodedExtraHeaderMapPrefix) {
		t.Fatalf("encoded header map is missing internal prefix")
	}

	headers, err := decodeExtraHeaderMap("test headers", encoded, func(name string) (string, bool) {
		if name == "HEADER_SECRET" {
			return "secret; with, delimiters", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("decodeExtraHeaderMap returned error: %v", err)
	}
	if headers["X-Composite"] != "first; second, third\xff" {
		t.Fatalf("delimiter or non-UTF-8 value changed: %#v", headers)
	}
	if headers["X-From-Env"] != "secret; with, delimiters" {
		t.Fatalf("environment reference was not resolved exactly: %#v", headers)
	}

	_, err = decodeExtraHeaderMap("test headers", encoded, func(name string) (string, bool) {
		return "secret\x7fvalue", name == "HEADER_SECRET"
	})
	if err == nil || !strings.Contains(err.Error(), "invalid HTTP header value") {
		t.Fatalf("expected resolved-value validation error, got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposed rejected resolved value: %v", err)
	}
}
