package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessExecuteScenarioWithStdioCommand(t *testing.T) {
	commandArgs := mockmcpserver.StdioServerCommand(t)
	runSimpleToolScenarioWithCommand(t, commandArgs)
}

func TestHarnessStdioOptInInitializesBeforeToolCallWhenControlPlaneOmitsNotification(t *testing.T) {
	t.Setenv("MOCK_MCP_REQUIRE_INITIALIZED", "1")
	runStdioInitializeThenToolScenario(t, true, "initialize\nnotifications/initialized\ntools/call\n")
}

func TestHarnessStdioLegacyDefaultDoesNotInjectInitializedNotification(t *testing.T) {
	t.Setenv("MOCK_MCP_REJECT_INITIALIZED", "1")
	runStdioInitializeThenToolScenario(t, false, "initialize\ntools/call\n")
}

func TestHarnessStdioServerDiscoverFallsBackToLegacyInitialize(t *testing.T) {
	t.Setenv("MOCK_MCP_REQUIRE_INITIALIZED", "1")
	messageLog := t.TempDir() + "/stdio-messages.log"
	t.Setenv("MOCK_MCP_MESSAGE_LOG", messageLog)

	discoverCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-discover",
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"discover-1",
				"method":"server/discover",
				"params":{
					"_meta":{
						"io.modelcontextprotocol/protocolVersion":"2026-07-28",
						"io.modelcontextprotocol/clientInfo":{"name":"chatgpt","version":"test"},
						"io.modelcontextprotocol/clientCapabilities":{}
					}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("discovery response = type %q status %d", resp.ResponseType, resp.ResponseCode)
				}
				var wire struct {
					ID    string `json:"id"`
					Error struct {
						Code int64 `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &wire); err != nil {
					tb.Fatalf("decode discovery error: %v", err)
				}
				if wire.ID != "discover-1" || wire.Error.Code != -32601 {
					tb.Fatalf("unexpected discovery error: %+v", wire)
				}
			},
		}},
	}
	initializeCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-initialize-after-discover",
			json.RawMessage(`{"jsonrpc":"2.0","id":"initialize-after-discover","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"chatgpt","version":"test"}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-initialize-after-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("initialize response = type %q status %d", resp.ResponseType, resp.ResponseCode)
				}
				if !bytes.Contains(resp.JSONResponse, []byte(`"protocolVersion":"2024-11-05"`)) {
					tb.Fatalf("unexpected initialize response: %s", string(resp.JSONResponse))
				}
			},
		}},
	}
	notificationCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-initialized-after-discover",
			json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-initialized-after-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("initialized response = type %q status %d", resp.ResponseType, resp.ResponseCode)
				}
			},
		}},
	}
	listCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-tools-list-after-discover",
			json.RawMessage(`{"jsonrpc":"2.0","id":"tools-list-after-discover","method":"tools/list","params":{}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-tools-list-after-discover"}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-tool-after-discover",
			json.RawMessage(`{"jsonrpc":"2.0","id":"tool-after-discover","method":"tools/call","params":{"name":"hello","arguments":{"name":"Ada"}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-tool-after-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("tool response = type %q status %d", resp.ResponseType, resp.ResponseCode)
				}
				if !bytes.Contains(resp.JSONResponse, []byte(`"message":"hello Ada"`)) {
					tb.Fatalf("unexpected tool response: %s", string(resp.JSONResponse))
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(mockmcpserver.StdioServerCommand(t)),
		harnesspkg.WithScenarioTimeout(5*time.Second),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(discoverCommand, initializeCommand, notificationCommand, listCommand, toolCommand)),
	)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got, want := string(messages), "server/discover\ninitialize\nnotifications/initialized\ntools/list\ntools/call\n"; got != want {
		t.Fatalf("unexpected stdio compatibility sequence: got %q want %q", got, want)
	}
}

func TestHarnessStdioServerDiscoverPreservesModernNativePath(t *testing.T) {
	t.Setenv("MOCK_MCP_SERVER_DISCOVER_MODE", "modern")
	messageLog := t.TempDir() + "/stdio-messages.log"
	t.Setenv("MOCK_MCP_MESSAGE_LOG", messageLog)

	discoverCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-modern-discover",
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"modern-discover-1",
				"method":"server/discover",
				"params":{
					"_meta":{
						"io.modelcontextprotocol/protocolVersion":"2026-07-28",
						"io.modelcontextprotocol/clientInfo":{"name":"chatgpt","version":"test"},
						"io.modelcontextprotocol/clientCapabilities":{}
					}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-modern-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				var wire struct {
					ID     string `json:"id"`
					Result struct {
						ResultType        string   `json:"resultType"`
						SupportedVersions []string `json:"supportedVersions"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &wire); err != nil {
					tb.Fatalf("decode native discovery response: %v", err)
				}
				if wire.ID != "modern-discover-1" || wire.Result.ResultType != "complete" ||
					len(wire.Result.SupportedVersions) != 1 || wire.Result.SupportedVersions[0] != "2026-07-28" {
					tb.Fatalf("unexpected native discovery response: %+v", wire)
				}
			},
		}},
	}
	listCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-modern-tools-list",
			json.RawMessage(`{"jsonrpc":"2.0","id":"modern-tools-list","method":"tools/list","params":{}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-modern-tools-list"}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-modern-tool",
			json.RawMessage(`{"jsonrpc":"2.0","id":"modern-tool-1","method":"tools/call","params":{"name":"hello","arguments":{"name":"Ada"}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-modern-tool"}},
	}

	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(mockmcpserver.StdioServerCommand(t)),
		harnesspkg.WithScenarioTimeout(4*time.Second),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(discoverCommand, listCommand, toolCommand)),
	)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got, want := string(messages), "server/discover\ntools/list\ntools/call\n"; got != want {
		t.Fatalf("modern discovery was translated unexpectedly: got %q want %q", got, want)
	}
}

func TestHarnessStdioServerDiscoverRecognizedModernErrorDoesNotFallback(t *testing.T) {
	t.Setenv("MOCK_MCP_SERVER_DISCOVER_MODE", "modern-error")
	messageLog := t.TempDir() + "/stdio-messages.log"
	t.Setenv("MOCK_MCP_MESSAGE_LOG", messageLog)

	discoverCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-modern-error-discover",
			json.RawMessage(`{"jsonrpc":"2.0","id":"modern-error-discover","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"chatgpt","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-modern-error-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				var wire struct {
					Error struct {
						Code int64 `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &wire); err != nil {
					tb.Fatalf("decode modern protocol error: %v", err)
				}
				if wire.Error.Code != -32022 {
					tb.Fatalf("modern protocol error code = %d, want -32022", wire.Error.Code)
				}
			},
		}},
	}
	listCommand := mocktunnelservice.CommandResponse{
		Command:           mocktunnelservice.NewCommand("cmd-modern-error-tools-list", json.RawMessage(`{"jsonrpc":"2.0","id":"modern-error-tools-list","method":"tools/list","params":{}}`), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-modern-error-tools-list"}},
	}

	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(mockmcpserver.StdioServerCommand(t)),
		harnesspkg.WithScenarioTimeout(4*time.Second),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(discoverCommand, listCommand)),
	)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got, want := string(messages), "server/discover\ntools/list\n"; got != want {
		t.Fatalf("recognized modern error triggered legacy initialization: got %q want %q", got, want)
	}
}

func TestHarnessStdioServerDiscoverTimeoutDiscardsLateResponseBeforeInitialize(t *testing.T) {
	t.Setenv("MOCK_MCP_SERVER_DISCOVER_MODE", "late-response")
	t.Setenv("MOCK_MCP_SERVER_DISCOVER_TIMEOUT_SECONDS", "3")
	t.Setenv("MOCK_MCP_REQUIRE_INITIALIZED", "1")
	messageLog := t.TempDir() + "/stdio-messages.log"
	t.Setenv("MOCK_MCP_MESSAGE_LOG", messageLog)

	discoverCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-timeout-discover",
			json.RawMessage(`{"jsonrpc":"2.0","id":"timeout-discover","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"chatgpt","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "cmd-timeout-discover",
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("timeout response = type %q status %d", resp.ResponseType, resp.ResponseCode)
				}
				var wire struct {
					Error struct {
						Code int64 `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &wire); err != nil {
					tb.Fatalf("decode timeout response: %v", err)
				}
				if wire.Error.Code != -32000 {
					tb.Fatalf("timeout response code = %d, want -32000", wire.Error.Code)
				}
			},
		}},
	}
	initializeCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"cmd-timeout-initialize",
			json.RawMessage(`{"jsonrpc":"2.0","id":"timeout-initialize","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"chatgpt","version":"test"}}}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-timeout-initialize"}},
	}
	notificationCommand := mocktunnelservice.CommandResponse{
		Command:           mocktunnelservice.NewCommand("cmd-timeout-initialized", json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-timeout-initialized"}},
	}
	listCommand := mocktunnelservice.CommandResponse{
		Command:           mocktunnelservice.NewCommand("cmd-timeout-tools-list", json.RawMessage(`{"jsonrpc":"2.0","id":"timeout-tools-list","method":"tools/list","params":{}}`), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-timeout-tools-list"}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command:           mocktunnelservice.NewCommand("cmd-timeout-tool", json.RawMessage(`{"jsonrpc":"2.0","id":"timeout-tool","method":"tools/call","params":{"name":"hello","arguments":{"name":"Ada"}}}`), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "cmd-timeout-tool"}},
	}

	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(mockmcpserver.StdioServerCommand(t)),
		harnesspkg.WithScenarioTimeout(9*time.Second),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(discoverCommand, initializeCommand, notificationCommand, listCommand, toolCommand)),
	)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got, want := string(messages), "server/discover\ninitialize\nnotifications/initialized\ntools/list\ntools/call\n"; got != want {
		t.Fatalf("unexpected timeout compatibility sequence: got %q want %q", got, want)
	}
}

func runStdioInitializeThenToolScenario(t *testing.T, sendInitializedNotification bool, wantMessages string) {
	t.Helper()

	const (
		initializeRequestID = "cmd-initialize"
		initializeCallID    = "initialize-1"
		toolRequestID       = "cmd-tool"
		toolCallID          = "tool-1"
	)

	messageLog := t.TempDir() + "/stdio-messages.log"
	t.Setenv("MOCK_MCP_MESSAGE_LOG", messageLog)

	initializeCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			initializeRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+initializeCallID+`",
				"method":"initialize",
				"params":{
					"protocolVersion":"2025-11-25",
					"capabilities":{},
					"clientInfo":{"name":"openai-mcp (Codex)","version":"1.0.0"}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initializeRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("initialize response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("initialize response code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+toolCallID+`",
				"method":"tools/call",
				"params":{"name":"echo","arguments":{"name":"Ada"}}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("tool response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("tool response code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}

	options := []harnesspkg.HarnessOption{
		harnesspkg.WithMCPCommand(mockmcpserver.StdioServerCommand(t)),
		harnesspkg.WithScenarioTimeout(3 * time.Second),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(initializeCommand, toolCommand),
		),
	}
	if sendInitializedNotification {
		options = append(options, harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.MCP.StdioSendInitializedNotification = true
		}))
	}
	h := harnesspkg.NewHarness(t, options...)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got := string(messages); got != wantMessages {
		t.Fatalf("unexpected stdio lifecycle sequence: got %q want %q", got, wantMessages)
	}
}

func TestHarnessStdioResponseDeadlineKeepsServingAfterTimedOutRequest(t *testing.T) {
	const (
		timedOutRequestID = "cmd-timeout"
		recoveryRequestID = "cmd-recovery"
		timedOutCallID    = "call-timeout"
		recoveryCallID    = "call-recovery"
	)

	invocationLog := t.TempDir() + "/stdio-invocations.log"
	t.Setenv("MOCK_MCP_DROP_RESPONSE_NAME", "timeout")
	t.Setenv("MOCK_MCP_SERVER_REQUEST_BEFORE_RESPONSE_NAME", "recovered")
	t.Setenv("MOCK_MCP_INVOCATION_LOG", invocationLog)

	timedOutCommand := mocktunnelservice.CommandResponse{
		Command: withResponseTimeout(t, mocktunnelservice.NewCommand(
			timedOutRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+timedOutCallID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{"name":"timeout","request_id":"timeout"}
				}
			}`),
			nil,
		), "2s"),
		NoResponseExpected: true,
	}
	recoveryCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			recoveryRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+recoveryCallID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{"name":"recovered","request_id":"recovered"}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: recoveryRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("recovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("recovery response code mismatch: got %d", resp.ResponseCode)
				}
				if !bytes.Contains(resp.JSONResponse, []byte(`"message":"hello recovered"`)) {
					tb.Fatalf("recovery response payload mismatch: %s", string(resp.JSONResponse))
				}
			},
		}},
	}

	var logs bytes.Buffer
	commandArgs := mockmcpserver.StdioServerCommand(t)
	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithLogWriter(&logs),
		harnesspkg.WithScenarioTimeout(8*time.Second),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelInfo
			// Keep one dispatcher worker so the recovery command can only
			// write after the timed-out lifecycle releases its stdio slot.
			cfg.MCP.MaxConcurrentRequests = 1
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(timedOutCommand, recoveryCommand),
		),
	)
	h.ExecuteScenarious(t)

	invocations, err := os.ReadFile(invocationLog)
	if err != nil {
		t.Fatalf("read stdio invocation log: %v", err)
	}
	if got := string(invocations); !strings.Contains(got, "timeout\n") || !strings.Contains(got, "recovered\n") {
		t.Fatalf("stdio server did not observe both commands: %q", got)
	}
	if !strings.Contains(logs.String(), "command response deadline reached; dropping without posting a response") {
		t.Fatalf("missing response deadline log:\n%s", logs.String())
	}
	// ExecuteScenarious stops the client before returning, and normal stdio
	// teardown emits the generic shutdown warning.
	if strings.Contains(logs.String(), `reason="stdio MCP command stdin write failed"`) ||
		strings.Contains(logs.String(), "file already closed") {
		t.Fatalf("stdio deadline closed shared transport:\n%s", logs.String())
	}
}

func runSimpleToolScenarioWithCommand(t *testing.T, commandArgs []string) {
	t.Helper()

	const (
		toolRequestID = "cmd-tool"
		callID        = "tool-1"
		userName      = "Ada"
	)
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+callID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{
						"name":"`+userName+`"
					}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tool call response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("tool call response code mismatch: %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("tool call missing resp_json payload")
				}
			},
		}},
	}

	options := []harnesspkg.HarnessOption{
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(toolCommand),
		),
	}

	h := harnesspkg.NewHarness(t, options...)
	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("expected three matched responses (initialize, initialized, tool); got %d", len(matched))
	}
	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 3 {
		t.Fatalf("expected three delivered commands; got %d", len(delivered))
	}
	var toolResponse mocktunnelservice.ReceivedResponse
	for _, resp := range matched {
		if resp.RequestID == toolRequestID {
			toolResponse = resp
			break
		}
	}
	if toolResponse.RequestID == "" {
		t.Fatalf("tool response for %s not recorded", toolRequestID)
	}
	var rpcPayload struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolResponse.JSONResponse, &rpcPayload); err != nil {
		t.Fatalf("decode tool response payload: %v", err)
	}
	msg, _ := rpcPayload.Result.StructuredContent["message"].(string)
	expectedMessage := fmt.Sprintf("hello %s", userName)
	if msg != expectedMessage {
		t.Fatalf("unexpected tool response message: got %q want %q", msg, expectedMessage)
	}
}

func withResponseTimeout(t testing.TB, command json.RawMessage, timeout string) json.RawMessage {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(command, &payload); err != nil {
		t.Fatalf("decode command payload: %v", err)
	}
	payload["response_timeout"] = timeout
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode command payload: %v", err)
	}
	return encoded
}
