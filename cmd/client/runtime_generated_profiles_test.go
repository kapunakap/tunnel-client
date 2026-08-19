package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestRuntimeAcceptsFullClientGeneratedProfiles(t *testing.T) {
	caBundle := writeRuntimeGeneratedProfileCABundle(t)
	env := map[string]string{
		"CONTROL_PLANE_API_KEY": "sk_test_key",
		"ENTERPRISE_CA_BUNDLE":  caBundle,
		"HTTPS_PROXY":           "http://127.0.0.1:8080",
	}

	for _, sample := range profileSamples() {
		sample := sample
		t.Run(sample.Name, func(t *testing.T) {
			generated, err := sample.Generate(sample.Example)
			require.NoError(t, err)
			require.Contains(t, string(generated), "admin_ui:\n  open_browser:")

			profileDir := t.TempDir()
			profilePath := filepath.Join(profileDir, sample.Name+".yaml")
			require.NoError(t, os.WriteFile(profilePath, generated, 0o600))

			fs := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
			runtimeconfig.RegisterFlags(fs, runtimeconfig.FlavorRuntime)
			require.NoError(t, fs.Parse([]string{
				"--profile", sample.Name,
				"--profile-dir", profileDir,
			}))

			cfg, err := runtimeconfig.LoadFromFlagSet(fs, runtimeGeneratedProfileLookupEnv(env))
			require.NoErrorf(t, err, "runtime rejected generated profile %s", sample.Name)
			require.Equal(t, sample.Name, cfg.Runtime.ProfileName)
			require.Equal(t, profilePath, cfg.Runtime.ProfilePath)
			require.Equal(t, profileDir, cfg.Runtime.ProfileDir)
			require.False(t, cfg.Runtime.ProfileFile)
			require.NotEmpty(t, cfg.MCP.ChannelBindings)
		})
	}
}

func writeRuntimeGeneratedProfileCABundle(t *testing.T) string {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	certificate := server.Certificate()
	require.NotNil(t, certificate)

	path := filepath.Join(t.TempDir(), "enterprise-ca.pem")
	payload := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	return path
}

func runtimeGeneratedProfileLookupEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
