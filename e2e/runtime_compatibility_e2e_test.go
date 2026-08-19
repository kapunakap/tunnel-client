package e2e_test

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

type runtimeCompatibilityVariant struct {
	name        string
	packagePath string
	binaryName  string
	flavor      string
}

type runtimeCompatibilityObservation struct {
	shared   runtimeCompatibilitySharedObservation
	uiStatus int
	uiBody   string
}

type runtimeCompatibilitySharedObservation struct {
	readyStatus         int
	readyBody           string
	healthStatus        int
	healthBody          string
	metricsStatus       int
	metricsHasLiveness  bool
	metricsHasReadiness bool
	responses           []runtimeCompatibilityResponse
	toolNames           []string
}

type runtimeCompatibilityResponse struct {
	requestID    string
	responseCode int
	responseType string
}

type runtimeCompatibilityRunOptions struct {
	env              map[string]string
	readinessSignals []string
}

// TestRuntimeCompatibilityMatchesFullClientSharedSurface launches the real
// complete client and customer runtime through the same profile-file and
// environment path. It compares only behavior that the runtime promises to
// retain; /ui remains a deliberate full-client-only surface.
func TestRuntimeCompatibilityMatchesFullClientSharedSurface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM compatibility check requires Unix process signals")
	}

	variants := []runtimeCompatibilityVariant{
		{
			name:        "full",
			packagePath: "./cmd/client",
			binaryName:  "tunnel-client",
			flavor:      "full",
		},
		{
			name:        "runtime",
			packagePath: "./cmd/client-runtime",
			binaryName:  "tunnel-client-runtime",
			flavor:      "runtime",
		},
	}

	profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
	observations := make(map[string]runtimeCompatibilityObservation, len(variants))
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			observations[variant.name] = runRuntimeCompatibilityVariant(
				t,
				variant,
				profilePath,
				healthURLFile,
				pidFile,
				runtimeCompatibilityRunOptions{},
			)
		})
	}

	assertRuntimeCompatibilityObservations(t, observations, "runtime")
}

// TestRuntimeCloudflaredCompatibilityMatchesFullClientSharedSurface applies
// the same production profile bytes to the full client and approved companion
// runtime while enabling the same deterministic bundled-cloudflared wrapper.
func TestRuntimeCloudflaredCompatibilityMatchesFullClientSharedSurface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM and fake cloudflared compatibility checks require Unix process signals")
	}

	profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
	cloudflaredPath := writeRuntimeArtifactCloudflaredWrapper(t)
	startedFile := filepath.Join(t.TempDir(), "cloudflared.started")
	exitFile := filepath.Join(t.TempDir(), "cloudflared.exit")
	options := runtimeCompatibilityRunOptions{
		env: map[string]string{
			"CLOUDFLARED_PATH":                   cloudflaredPath,
			"CLOUDFLARED_TUNNEL_TOKEN":           "runtime-artifact-cloudflared-token",
			"GO_WANT_RUNTIME_CLOUDFLARED_HELPER": "1",
			"RUNTIME_CLOUDFLARED_EXIT_FILE":      exitFile,
			"RUNTIME_CLOUDFLARED_STARTED_FILE":   startedFile,
			"RUNTIME_E2E_HELPER_BINARY":          os.Args[0],
		},
		readinessSignals: []string{runtimeArtifactCloudflaredReadySignal},
	}
	variants := []runtimeCompatibilityVariant{
		{
			name:        "full",
			packagePath: "./cmd/client",
			binaryName:  "tunnel-client",
			flavor:      "full",
		},
		{
			name:        "runtime-cloudflared",
			packagePath: "./cmd/client-runtime-cloudflared",
			binaryName:  "tunnel-client-runtime-cloudflared",
			flavor:      "runtime-cloudflared",
		},
	}

	observations := make(map[string]runtimeCompatibilityObservation, len(variants))
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			_ = os.Remove(startedFile)
			_ = os.Remove(exitFile)
			observations[variant.name] = runRuntimeCompatibilityVariant(
				t,
				variant,
				profilePath,
				healthURLFile,
				pidFile,
				options,
			)
			_, err := os.Stat(startedFile)
			require.NoError(t, err, "fake cloudflared startup marker should exist after readiness")
		})
	}

	assertRuntimeCompatibilityObservations(t, observations, "runtime-cloudflared")
}

func assertRuntimeCompatibilityObservations(
	t *testing.T,
	observations map[string]runtimeCompatibilityObservation,
	runtimeVariant string,
) {
	t.Helper()

	require.Len(t, observations, 2)
	full := observations["full"]
	customerRuntime := observations[runtimeVariant]
	require.Equalf(t, full.shared, customerRuntime.shared, "full client and %s shared behavior diverged", runtimeVariant)

	require.Equal(t, http.StatusOK, full.uiStatus, "full client should keep its admin UI")
	require.Contains(t, full.uiBody, "tunnel-client")
	require.Equal(t, http.StatusNotFound, customerRuntime.uiStatus, "customer runtime must not expose the admin UI")
}

func runRuntimeCompatibilityVariant(
	t *testing.T,
	variant runtimeCompatibilityVariant,
	profilePath string,
	healthURLFile string,
	pidFile string,
	options runtimeCompatibilityRunOptions,
) runtimeCompatibilityObservation {
	t.Helper()

	controlPlane, mcpServer := newRuntimeArtifactMocks(t)
	binary := buildRuntimeArtifact(t, variant.packagePath, variant.binaryName, variant.flavor)

	overrides := make(map[string]string, len(options.env)+3)
	for key, value := range options.env {
		overrides[key] = value
	}
	overrides["TUNNEL_CLIENT_PROFILE_FILE"] = profilePath
	overrides["RUNTIME_COMPAT_CONTROL_PLANE_URL"] = controlPlane.BaseURL().String()
	overrides["RUNTIME_COMPAT_MCP_URL"] = mcpServer.BaseURL().String()
	proc := startRuntimeArtifactWithEnv(t, binary, overrides, "run")

	healthBaseURL := waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	waitForRuntimeArtifactOutput(t, proc, "PID file", runtimeArtifactPIDFileSignal)
	assertRuntimeCompatibilityPIDFile(t, proc, pidFile)
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	readinessSignals := append([]string{
		runtimeArtifactMCPReadySignal,
		runtimeArtifactOAuthReadySignal,
	}, options.readinessSignals...)
	waitForRuntimeArtifactOutput(
		t,
		proc,
		"shared readiness",
		readinessSignals...,
	)

	observation := observeRuntimeCompatibilitySurface(t, healthBaseURL, controlPlane, mcpServer)
	require.True(
		t,
		runtimeCompatibilityControlPlaneSawAPIKey(controlPlane),
		"control-plane mock never observed the env-resolved API key",
	)
	require.NotContains(t, proc.output.String(), runtimeArtifactAPIKey, "runtime output leaked the control-plane API key")
	require.NotContains(t, proc.output.String(), "Bearer "+runtimeArtifactAPIKey, "runtime output leaked the bearer token")

	require.NoError(t, proc.cmd.Process.Signal(syscall.SIGTERM))
	waitErr, exited := proc.wait(10 * time.Second)
	require.Truef(t, exited, "%s did not exit after SIGTERM; output:\n%s", variant.name, proc.output.String())
	require.NoErrorf(t, waitErr, "%s did not shut down cleanly after SIGTERM; output:\n%s", variant.name, proc.output.String())
	assertRuntimeCompatibilityRemoved(t, pidFile, "PID file")
	assertRuntimeCompatibilityRemoved(t, healthURLFile, "health URL file")

	return observation
}

func writeRuntimeCompatibilityProfile(t *testing.T) (profilePath, healthURLFile, pidFile string) {
	t.Helper()

	dir := t.TempDir()
	profilePath = filepath.Join(dir, "runtime-compatibility.yaml")
	healthURLFile = filepath.Join(dir, "health.url")
	pidFile = filepath.Join(dir, "tunnel-client.pid")
	profile := strings.Join([]string{
		"config_version: 1",
		"control_plane:",
		"  base_url: env:RUNTIME_COMPAT_CONTROL_PLANE_URL",
		"  tunnel_id: " + runtimeArtifactYAMLScalar(runtimeArtifactTunnelID),
		"  api_key: env:CONTROL_PLANE_API_KEY",
		"mcp:",
		"  server_urls:",
		"    - channel: main",
		"      url: env:RUNTIME_COMPAT_MCP_URL",
		"health:",
		"  listen_addr: 127.0.0.1:0",
		"  url_file: " + runtimeArtifactYAMLScalar(healthURLFile),
		"process:",
		"  pid_file: " + runtimeArtifactYAMLScalar(pidFile),
		"admin_ui:",
		"  open_browser: false",
		"log:",
		"  level: info",
		"  format: struct-text",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0o600))
	return profilePath, healthURLFile, pidFile
}

func observeRuntimeCompatibilitySurface(
	t *testing.T,
	healthBaseURL string,
	controlPlane *mocktunnelservice.MockTunnelService,
	mcpServer *mockmcpserver.MockMCPServer,
) runtimeCompatibilityObservation {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	readyStatus, readyBody := runtimeArtifactResponse(t, client, healthBaseURL+"/readyz")
	healthStatus, healthBody := runtimeArtifactResponse(t, client, healthBaseURL+"/healthz")
	metricsStatus, metricsBody := runtimeArtifactResponse(t, client, healthBaseURL+"/metrics")
	uiStatus, uiBody := runtimeArtifactResponse(t, client, healthBaseURL+"/ui")

	require.Equal(t, http.StatusOK, readyStatus, "readyz should be ready")
	require.Equal(t, "ready", strings.TrimSpace(readyBody))
	require.Equal(t, http.StatusOK, healthStatus, "healthz should be live")
	require.Equal(t, "live", strings.TrimSpace(healthBody))
	require.Equal(t, http.StatusOK, metricsStatus, "metrics should be available")
	require.Contains(t, metricsBody, "liveness")
	require.Contains(t, metricsBody, "readiness")

	responses := controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, responses, 3, "initialize, initialized, and tool responses should all be matched")
	responseSignatures := make([]runtimeCompatibilityResponse, 0, len(responses))
	for _, response := range responses {
		responseSignatures = append(responseSignatures, runtimeCompatibilityResponse{
			requestID:    response.RequestID,
			responseCode: response.ResponseCode,
			responseType: response.ResponseType,
		})
	}
	sort.Slice(responseSignatures, func(i, j int) bool {
		return responseSignatures[i].requestID < responseSignatures[j].requestID
	})

	requests := mcpServer.ReceivedRequests()
	require.Len(t, requests, 1)
	toolNames := make([]string, 0, len(requests))
	for _, request := range requests {
		toolNames = append(toolNames, request.Tool)
	}
	sort.Strings(toolNames)

	return runtimeCompatibilityObservation{
		shared: runtimeCompatibilitySharedObservation{
			readyStatus:         readyStatus,
			readyBody:           strings.TrimSpace(readyBody),
			healthStatus:        healthStatus,
			healthBody:          strings.TrimSpace(healthBody),
			metricsStatus:       metricsStatus,
			metricsHasLiveness:  strings.Contains(metricsBody, "liveness"),
			metricsHasReadiness: strings.Contains(metricsBody, "readiness"),
			responses:           responseSignatures,
			toolNames:           toolNames,
		},
		uiStatus: uiStatus,
		uiBody:   uiBody,
	}
}

func assertRuntimeCompatibilityPIDFile(t *testing.T, proc *runtimeArtifactProcess, pidFile string) {
	t.Helper()

	pidBytes, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	require.Equal(t, proc.cmd.Process.Pid, pid)
}

func runtimeCompatibilityControlPlaneSawAPIKey(controlPlane *mocktunnelservice.MockTunnelService) bool {
	for _, request := range controlPlane.ReceivedHTTPRequests() {
		if request.Headers.Get("Authorization") == "Bearer "+runtimeArtifactAPIKey {
			return true
		}
	}
	return false
}

func assertRuntimeCompatibilityRemoved(t *testing.T, path, description string) {
	t.Helper()

	_, err := os.Stat(path)
	require.ErrorIsf(t, err, os.ErrNotExist, "%s should be removed on SIGTERM", description)
}
