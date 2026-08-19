package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

const (
	adapterTestTunnelID = "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	adapterTestAPIKey   = "sk_test_key"
)

func TestFullAdapterCoversEveryRuntimeConfigField(t *testing.T) {
	runtimeType := reflect.TypeOf(runtimeconfig.Config{})
	fullType := reflect.TypeOf(Config{})
	for runtimeField := range runtimeType.Fields() {
		if runtimeField.Name == "Harpoon" {
			// Harpoon keeps one full-only CapturePayloads bit in the public
			// compatibility adapter; runtimeCoreFromFull covers it below.
			continue
		}
		fullField, ok := fullType.FieldByName(runtimeField.Name)
		if !ok {
			t.Fatalf("full config adapter is missing runtime field %q", runtimeField.Name)
		}
		if fullField.Type != runtimeField.Type {
			t.Fatalf("full config field %q type = %s, want %s", runtimeField.Name, fullField.Type, runtimeField.Type)
		}
	}
	runtimeHarpoonType := reflect.TypeOf(runtimeconfig.HarpoonConfig{})
	fullHarpoonType := reflect.TypeOf(HarpoonConfig{})
	for runtimeField := range runtimeHarpoonType.Fields() {
		fullField, ok := fullHarpoonType.FieldByName(runtimeField.Name)
		if !ok {
			t.Fatalf("full Harpoon adapter is missing runtime field %q", runtimeField.Name)
		}
		if fullField.Type != runtimeField.Type {
			t.Fatalf("full Harpoon field %q type = %s, want %s", runtimeField.Name, fullField.Type, runtimeField.Type)
		}
	}
}

func TestFullAdapterCopiesEveryRuntimeConfigFieldValue(t *testing.T) {
	coreValue := adapterSentinelValue(t, reflect.TypeOf(runtimeconfig.Config{}), 1)
	core := coreValue.Interface().(runtimeconfig.Config)
	full := fullConfigFromRuntime(&core, CloudflaredConfig{}, AdminUIConfig{}, false, ProxyHealthConfig{})

	runtimeValue := reflect.ValueOf(core)
	fullValue := reflect.ValueOf(*full)
	for runtimeField := range reflect.TypeOf(core).Fields() {
		if runtimeField.Name == "Harpoon" {
			continue
		}
		want := runtimeValue.FieldByName(runtimeField.Name)
		if want.IsZero() {
			t.Fatalf("test sentinel for runtime field %q is zero", runtimeField.Name)
		}
		got := fullValue.FieldByName(runtimeField.Name)
		if !adapterValuesEqual(got, want) {
			t.Fatalf("full config adapter did not copy runtime field %q", runtimeField.Name)
		}
	}

	runtimeHarpoon := reflect.ValueOf(core.Harpoon)
	fullHarpoon := reflect.ValueOf(full.Harpoon)
	for runtimeField := range reflect.TypeOf(core.Harpoon).Fields() {
		want := runtimeHarpoon.FieldByName(runtimeField.Name)
		if want.IsZero() {
			t.Fatalf("test sentinel for runtime Harpoon field %q is zero", runtimeField.Name)
		}
		got := fullHarpoon.FieldByName(runtimeField.Name)
		if !adapterValuesEqual(got, want) {
			t.Fatalf("full Harpoon adapter did not copy runtime field %q", runtimeField.Name)
		}
	}
}

func adapterSentinelValue(t *testing.T, typ reflect.Type, seed int) reflect.Value {
	t.Helper()
	value := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.Bool:
		value.SetBool(true)
	case reflect.String:
		value.SetString(fmt.Sprintf("sentinel-%d", seed))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(int64(seed + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value.SetUint(uint64(seed + 1))
	case reflect.Float32, reflect.Float64:
		value.SetFloat(float64(seed + 1))
	case reflect.Complex64, reflect.Complex128:
		value.SetComplex(complex(float64(seed+1), float64(seed+2)))
	case reflect.Ptr:
		value.Set(reflect.New(typ.Elem()))
	case reflect.Func:
		value.Set(reflect.MakeFunc(typ, func([]reflect.Value) []reflect.Value {
			results := make([]reflect.Value, typ.NumOut())
			for i := range results {
				results[i] = reflect.Zero(typ.Out(i))
			}
			return results
		}))
	case reflect.Chan:
		value.Set(reflect.MakeChan(typ, 1))
	case reflect.Slice:
		slice := reflect.MakeSlice(typ, 1, 1)
		slice.Index(0).Set(adapterSentinelValue(t, typ.Elem(), seed+1))
		value.Set(slice)
	case reflect.Array:
		for i := 0; i < typ.Len(); i++ {
			value.Index(i).Set(adapterSentinelValue(t, typ.Elem(), seed+i+1))
		}
	case reflect.Map:
		key := adapterSentinelValue(t, typ.Key(), seed+1)
		element := adapterSentinelValue(t, typ.Elem(), seed+2)
		value.Set(reflect.MakeMapWithSize(typ, 1))
		value.SetMapIndex(key, element)
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			value.Field(i).Set(adapterSentinelValue(t, field.Type, seed+i+1))
		}
	default:
		t.Fatalf("unsupported adapter sentinel kind %s for %s", typ.Kind(), typ)
	}
	return value
}

func adapterValuesEqual(got reflect.Value, want reflect.Value) bool {
	if !got.IsValid() || !want.IsValid() {
		return got.IsValid() == want.IsValid()
	}
	if got.Type() != want.Type() {
		return false
	}
	switch want.Kind() {
	case reflect.Func:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return got.Pointer() == want.Pointer()
	case reflect.Chan:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return got.Pointer() == want.Pointer()
	case reflect.Ptr:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return adapterValuesEqual(got.Elem(), want.Elem())
	case reflect.Interface:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return adapterValuesEqual(got.Elem(), want.Elem())
	case reflect.Struct:
		for i := 0; i < want.NumField(); i++ {
			if want.Type().Field(i).PkgPath != "" {
				continue
			}
			if !adapterValuesEqual(got.Field(i), want.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if got.IsNil() != want.IsNil() || got.Len() != want.Len() {
			return false
		}
		for i := 0; i < want.Len(); i++ {
			if !adapterValuesEqual(got.Index(i), want.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Array:
		for i := 0; i < want.Len(); i++ {
			if !adapterValuesEqual(got.Index(i), want.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if got.IsNil() != want.IsNil() || got.Len() != want.Len() {
			return false
		}
		iter := want.MapRange()
		for iter.Next() {
			gotValue := got.MapIndex(iter.Key())
			if !gotValue.IsValid() || !adapterValuesEqual(gotValue, iter.Value()) {
				return false
			}
		}
		return true
	default:
		if !got.CanInterface() || !want.CanInterface() {
			return false
		}
		return reflect.DeepEqual(got.Interface(), want.Interface())
	}
}

func TestFullAndRuntimeLoadSharedEffectiveConfigParity(t *testing.T) {
	profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  api_key: env:PROFILE_API_KEY
  base_url: https://profile.example.invalid
mcp:
  server_urls:
    - url: https://profile-mcp.example.invalid/mcp
health:
  listen_addr: 127.0.0.1:7777
`)
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{
			name: "required environment plus defaults",
			args: []string{"--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"},
			env:  map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey},
		},
		{
			name: "environment",
			args: nil,
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":   adapterTestAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": adapterTestTunnelID,
				"MCP_SERVER_URL":          "https://env-mcp.example.invalid/mcp",
				"HEALTH_LISTEN_ADDR":      "127.0.0.1:8888",
			},
		},
		{
			name: "profile",
			args: []string{"--config", profile},
			env:  map[string]string{"PROFILE_API_KEY": adapterTestAPIKey},
		},
		{
			name: "flag over environment over profile",
			args: []string{
				"--config", profile,
				"--control-plane.tunnel-id", adapterTestTunnelID,
				"--mcp.server-url", "https://flag-mcp.example.invalid/mcp",
			},
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":  adapterTestAPIKey,
				"CONTROL_PLANE_BASE_URL": "https://env.example.invalid",
				"HEALTH_LISTEN_ADDR":     "127.0.0.1:9999",
				"PROFILE_API_KEY":        adapterTestAPIKey,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := lookupEnvMap(tc.env)
			full, err := Load(tc.args, lookup)
			if err != nil {
				t.Fatalf("full Load returned error: %v", err)
			}
			runtime, err := runtimeconfig.Load(tc.args, runtimeconfig.FlavorRuntime, lookup)
			if err != nil {
				t.Fatalf("runtime Load returned error: %v", err)
			}
			assertSharedRuntimeParity(t, full, runtime)
		})
	}
}

func TestFullAndRuntimeAcceptDefaultEquivalentFullOnlyEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "ALLOW_REMOTE_UI", value: "false"},
		{name: "OPEN_WEB_UI", value: "false"},
		{name: "ADMIN_UI_LOG_BUFFER_EVENTS", value: "2000"},
		{name: "PROXY_CHECK_INTERVAL", value: "60s"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m0s"},
		{name: "HARPOON_CAPTURE_PAYLOADS", value: "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			args := []string{
				"--control-plane.tunnel-id", adapterTestTunnelID,
				"--mcp.server-url", "https://mcp.example.invalid/mcp",
			}
			lookup := lookupEnvMap(map[string]string{
				"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
				tc.name:                 tc.value,
			})
			full, runtime := loadParityPair(t, args, lookup)
			assertSharedRuntimeParity(t, full, runtime)
		})
	}
}

func TestFullAndRuntimeSharedProductionInputsParity(t *testing.T) {
	headerFile := writeAdapterSecret(t, "file-header-value\n")
	t.Run("headers proxies and repeatable flags", func(t *testing.T) {
		args := []string{
			"--control-plane.tunnel-id", adapterTestTunnelID,
			"--control-plane.poll-channel", "main",
			"--control-plane.poll-channel", "tools",
			"--control-plane.extra-headers", "X-Control-Env: env:CONTROL_HEADER",
			"--control-plane.extra-headers", "X-Control-File: file:" + headerFile,
			"--http-proxy", "env:GLOBAL_PROXY",
			"--control-plane.http-proxy", "env:CONTROL_PROXY",
			"--mcp.server-url", "channel=main,url=https://main-mcp.example.invalid/mcp",
			"--mcp.server-url", "channel=tools,url=https://tools-mcp.example.invalid/mcp",
			"--mcp.extra-headers", "X-MCP-Env: env:MCP_HEADER",
			"--mcp.extra-headers", "X-MCP-File: file:" + headerFile,
			"--mcp.discovery-extra-headers", "X-Discovery: env:DISCOVERY_HEADER",
			"--mcp.http-proxy", "env:MCP_PROXY",
			"--harpoon.target", "label=auth,url=https://auth.example.invalid/token",
			"--harpoon.target", "label=metadata,url=https://auth.example.invalid/.well-known/oauth-authorization-server",
			"--harpoon.http-proxy", "env:HARPOON_PROXY",
		}
		lookup := lookupEnvMap(map[string]string{
			"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
			"CONTROL_HEADER":        "control-env-value",
			"MCP_HEADER":            "mcp-env-value",
			"DISCOVERY_HEADER":      "discovery-env-value",
			"GLOBAL_PROXY":          "http://global-proxy.example.invalid:8080",
			"CONTROL_PROXY":         "http://control-proxy.example.invalid:8080",
			"MCP_PROXY":             "http://mcp-proxy.example.invalid:8080",
			"HARPOON_PROXY":         "http://harpoon-proxy.example.invalid:8080",
		})
		full, runtime := loadParityPair(t, args, lookup)
		assertSharedRuntimeParity(t, full, runtime)
	})

	t.Run("profile secret references TLS and mounted paths", func(t *testing.T) {
		certPath, keyPath := writeAdapterClientCertificate(t)
		apiKeyPath := writeAdapterSecret(t, adapterTestAPIKey+"\n")
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		relativeCertPath, err := filepath.Rel(cwd, certPath)
		if err != nil {
			t.Fatalf("relative cert path: %v", err)
		}
		relativeKeyPath, err := filepath.Rel(cwd, keyPath)
		if err != nil {
			t.Fatalf("relative key path: %v", err)
		}
		relativeAPIKeyPath, err := filepath.Rel(cwd, apiKeyPath)
		if err != nil {
			t.Fatalf("relative API key path: %v", err)
		}
		profile := writeAdapterConfig(t, `
config_version: 1
ca_bundle: `+relativeCertPath+`
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: file:`+relativeAPIKeyPath+`
  client_cert: `+relativeCertPath+`
  client_key: `+relativeKeyPath+`
  extra_headers:
    X-Profile-Secret: file:`+apiKeyPath+`
mcp:
  server_urls:
    - channel: main
      url: https://mcp.example.invalid/mcp
      client_cert: `+relativeCertPath+`
      client_key: `+relativeKeyPath+`
  extra_headers:
    X-MCP-Profile: file:`+apiKeyPath+`
health:
  url_file: relative/health.url
process:
  pid_file: relative/client.pid
`)
		full, runtime := loadParityPair(t, []string{"--config", profile}, lookupEnvMap(nil))
		assertSharedRuntimeParity(t, full, runtime)
		if full.TLS == nil || full.TLS.Path != relativeCertPath {
			t.Fatalf("full TLS path = %#v, want %q", full.TLS, relativeCertPath)
		}
		if full.Health.URLFile != "relative/health.url" || full.Process.PIDFile != "relative/client.pid" {
			t.Fatalf("relative mounted paths changed: health=%q pid=%q", full.Health.URLFile, full.Process.PIDFile)
		}
	})
}

func TestRuntimeAcceptsDisabledFullOnlyProfileValuesUnchanged(t *testing.T) {
	for _, interval := range []string{"60s", "1m", "1m0s"} {
		t.Run("proxy.check_interval="+interval, func(t *testing.T) {
			profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  allow_remote: false
  open_browser: false
  log_buffer_events: 2000
harpoon:
  capture_payloads: false
proxy:
  check_interval: `+interval+`
`)
			args := []string{"--config", profile}
			lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})
			full, err := Load(args, lookup)
			if err != nil {
				t.Fatalf("full Load returned error: %v", err)
			}
			runtime, err := runtimeconfig.Load(args, runtimeconfig.FlavorRuntime, lookup)
			if err != nil {
				t.Fatalf("runtime rejected disabled full-only profile values: %v", err)
			}
			assertSharedRuntimeParity(t, full, runtime)
		})
	}
}

func TestRuntimeRejectsNonDefaultFullOnlyProfileValues(t *testing.T) {
	cases := map[string]string{
		"admin_ui.allow_remote":      "admin_ui:\n  allow_remote: true",
		"admin_ui.open_browser":      "admin_ui:\n  open_browser: true",
		"admin_ui.log_buffer_events": "admin_ui:\n  log_buffer_events: 2001",
		"harpoon.capture_payloads":   "harpoon:\n  capture_payloads: true",
		"proxy.check_interval":       "proxy:\n  check_interval: 61s",
	}
	for want, extension := range cases {
		t.Run(want, func(t *testing.T) {
			profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
`+extension+"\n")
			args := []string{"--config", profile}
			lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})
			if _, err := Load(args, lookup); err != nil {
				t.Fatalf("full Load rejected its extension: %v", err)
			}
			_, err := runtimeconfig.Load(args, runtimeconfig.FlavorRuntime, lookup)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("runtime error = %v, want rejection mentioning %q", err, want)
			}
		})
	}
}

func TestFullOnlyExtensionPrecedenceAndCloudflaredAdapter(t *testing.T) {
	profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  allow_remote: true
  open_browser: true
  log_buffer_events: 321
harpoon:
  capture_payloads: true
proxy:
  check_interval: 45s
cloudflared:
  token: env:PROFILE_CLOUDFLARED_TOKEN
  path: /profile/cloudflared
  ready_timeout: 20s
`)
	cfg, err := Load([]string{
		"--config", profile,
		"--admin-ui.log-buffer-events", "456",
		"--proxy.check-interval", "30s",
		"--cloudflared.path", "/flag/cloudflared",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
		"PROFILE_CLOUDFLARED_TOKEN": "profile-token",
		"OPEN_WEB_UI":               "false",
		"HARPOON_CAPTURE_PAYLOADS":  "false",
	}))
	if err != nil {
		t.Fatalf("full Load returned error: %v", err)
	}
	if !cfg.AdminUI.AllowRemote || cfg.AdminUI.OpenBrowser || cfg.AdminUI.LogBufferEvents != 456 {
		t.Fatalf("unexpected full Admin UI extension: %#v", cfg.AdminUI)
	}
	if cfg.Harpoon.CapturePayloads {
		t.Fatalf("environment should override profile Harpoon capture")
	}
	if cfg.ProxyHealth.CheckInterval.String() != "30s" {
		t.Fatalf("unexpected proxy health interval: %s", cfg.ProxyHealth.CheckInterval)
	}
	if cfg.Cloudflared.Token != "profile-token" || cfg.Cloudflared.Path != "/flag/cloudflared" || cfg.Cloudflared.ReadyTimeout.String() != "20s" {
		t.Fatalf("unexpected Cloudflared adapter: %#v", cfg.Cloudflared)
	}
}

func TestFullProfileValidationKeepsFullOnlyFields(t *testing.T) {
	profile := []byte(`
config_version: 1
admin_ui:
  allow_remote: true
  open_browser: true
  log_buffer_events: 321
harpoon:
  capture_payloads: true
proxy:
  check_interval: 45s
cloudflared:
  managed: true
`)
	if err := ValidateProfileBytes("full.yaml", profile); err != nil {
		t.Fatalf("full profile validation rejected full-only fields: %v", err)
	}
	if err := runtimeconfig.ValidateProfileBytes("runtime.yaml", profile); err == nil {
		t.Fatal("runtime profile validation accepted non-default full-only fields")
	}
}

func loadParityPair(t *testing.T, args []string, lookup func(string) (string, bool)) (*Config, *runtimeconfig.Config) {
	t.Helper()
	full, err := Load(args, lookup)
	if err != nil {
		t.Fatalf("full Load returned error: %v", err)
	}
	runtime, err := runtimeconfig.Load(args, runtimeconfig.FlavorRuntime, lookup)
	if err != nil {
		t.Fatalf("runtime Load returned error: %v", err)
	}
	return full, runtime
}

func assertSharedRuntimeParity(t *testing.T, full *Config, runtime *runtimeconfig.Config) {
	t.Helper()
	got := runtimeCoreFromFull(full)
	want := *runtime
	gotTLSEvidence := tlsEvidence(got)
	wantTLSEvidence := tlsEvidence(want)
	clearLoadedTLS(&got)
	clearLoadedTLS(&want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared effective config differs:\nfull: %#v\nruntime: %#v", got, want)
	}
	if !reflect.DeepEqual(gotTLSEvidence, wantTLSEvidence) {
		t.Fatalf("shared TLS evidence differs:\nfull: %#v\nruntime: %#v", gotTLSEvidence, wantTLSEvidence)
	}
}

func tlsEvidence(cfg runtimeconfig.Config) []string {
	evidence := make([]string, 0, 8)
	if cfg.TLS != nil {
		evidence = append(evidence, "bundle="+cfg.TLS.Path)
	}
	if cert := cfg.ControlPlane.ClientCertificate; cert != nil {
		evidence = append(evidence, "control-plane="+cert.CertPath+"|"+cert.KeyPath)
	}
	if cert := cfg.MCP.ClientCertificate; cert != nil {
		evidence = append(evidence, "mcp-default="+cert.CertPath+"|"+cert.KeyPath)
	}
	for _, binding := range cfg.MCP.ChannelBindings {
		if cert := binding.ClientCertificate; cert != nil {
			evidence = append(evidence, "mcp-"+binding.Channel.String()+"="+cert.CertPath+"|"+cert.KeyPath)
		}
	}
	return evidence
}

func clearLoadedTLS(cfg *runtimeconfig.Config) {
	cfg.TLS = nil
	cfg.ControlPlane.ClientCertificate = nil
	cfg.MCP.ClientCertificate = nil
	cfg.MCP.ChannelBindings = append([]runtimeconfig.MCPChannelBinding(nil), cfg.MCP.ChannelBindings...)
	for i := range cfg.MCP.ChannelBindings {
		cfg.MCP.ChannelBindings[i].ClientCertificate = nil
	}
}

func runtimeCoreFromFull(cfg *Config) runtimeconfig.Config {
	return runtimeconfig.Config{
		ControlPlane: cfg.ControlPlane,
		Logging:      cfg.Logging,
		Health:       cfg.Health,
		Process:      cfg.Process,
		MCP:          cfg.MCP,
		Harpoon: runtimeconfig.HarpoonConfig{
			AllowPlaintextHTTP:   cfg.Harpoon.AllowPlaintextHTTP,
			MaxResponseBytes:     cfg.Harpoon.MaxResponseBytes,
			MaxRedirects:         cfg.Harpoon.MaxRedirects,
			AdditionalTransports: cfg.Harpoon.AdditionalTransports,
			Targets:              cfg.Harpoon.Targets,
			HostClassifier:       cfg.Harpoon.HostClassifier,
			HTTPProxy:            cfg.Harpoon.HTTPProxy,
			HTTPProxySource:      cfg.Harpoon.HTTPProxySource,
		},
		TLS:     cfg.TLS,
		Runtime: cfg.Runtime,
	}
}

func writeAdapterSecret(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return path
}

func writeAdapterClientCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "adapter-test"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return certPath, keyPath
}

func writeAdapterConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
