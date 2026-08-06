package fx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/controlplane/internal"
	"github.com/openai/tunnel-client/pkg/mcpserverinfo"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestBuildMCPServerInfoHeaderAdvertisesEffectiveBindings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		cfg            config.MCPConfig
		harpoonEnabled bool
		want           string
	}{
		{
			name:           "stdio main with enabled Harpoon",
			harpoonEnabled: true,
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportStdio,
					Command:       "/private/stdio-command",
				}},
			},
			want: `{"version":1,"channels":[{"name":"main","proc_affinity":true},{"name":"harpoon","proc_affinity":true}]}`,
		},
		{
			name: "remote streamable HTTP main without Harpoon",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     &url.URL{Scheme: "https", Host: "private.example"},
				}},
			},
			want: `{"version":1,"channels":[{"name":"main"}]}`,
		},
		{
			name:           "remote streamable HTTP main with enabled Harpoon",
			harpoonEnabled: true,
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
				}},
			},
			want: `{"version":1,"channels":[{"name":"main"},{"name":"harpoon","proc_affinity":true}]}`,
		},
		{
			name: "legacy in-memory main without Harpoon",
			cfg: config.MCPConfig{
				TransportKind: config.MCPTransportInMemory,
			},
			want: `{"version":1,"channels":[{"name":"main","proc_affinity":true}]}`,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildMCPServerInfoHeader(&testCase.cfg, testCase.harpoonEnabled)
			if err != nil {
				t.Fatalf("buildMCPServerInfoHeader returned error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("header = %s, want %s", got, testCase.want)
			}
			if strings.Contains(got, "private") {
				t.Fatalf("header leaked transport details: %s", got)
			}
		})
	}
}

func TestMCPServerInfoHeaderProviderTracksHarpoonEnablement(t *testing.T) {
	t.Parallel()

	enabled := false
	provider, err := newMCPServerInfoHeaderProviderForPollChannels(
		&config.MCPConfig{TransportKind: config.MCPTransportHTTPStreamable},
		nil,
		func() bool { return enabled },
	)
	if err != nil {
		t.Fatalf("newMCPServerInfoHeaderProvider returned error: %v", err)
	}

	got, err := provider()
	if err != nil {
		t.Fatalf("provider without Harpoon returned error: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"main"}]}`; got != want {
		t.Fatalf("provider without Harpoon = %s, want %s", got, want)
	}

	enabled = true
	got, err = provider()
	if err != nil {
		t.Fatalf("provider with Harpoon returned error: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"main"},{"name":"harpoon","proc_affinity":true}]}`; got != want {
		t.Fatalf("provider with Harpoon = %s, want %s", got, want)
	}
}

func TestBuildMCPServerInfoHeaderHarpoonOnly(t *testing.T) {
	t.Parallel()
	got, err := buildMCPServerInfoHeader(&config.MCPConfig{AllowNoMain: true}, true)
	if err != nil {
		t.Fatalf("buildMCPServerInfoHeader returned error: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"harpoon","proc_affinity":true}]}`; got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestMCPServerInfoHeaderProviderFiltersDisabledHarpoon(t *testing.T) {
	t.Parallel()
	provider, err := newMCPServerInfoHeaderProviderForPollChannels(
		&config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{{
			Channel:       types.DefaultChannel,
			TransportKind: config.MCPTransportHTTPStreamable,
		}}},
		&config.ControlPlaneConfig{PollChannelsConfigured: true, PollChannels: []types.Channel{types.DefaultChannel}},
		func() bool { return true },
	)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got, err := provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"main"}]}`; got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestMCPServerInfoHeaderProviderReservesFutureHarpoonChannel(t *testing.T) {
	t.Parallel()

	bindings := make([]config.MCPChannelBinding, 0, mcpserverinfo.MaxChannels)
	bindings = append(bindings, config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
	})
	for index := 1; index < mcpserverinfo.MaxChannels; index++ {
		bindings = append(bindings, config.MCPChannelBinding{
			Channel:       types.Channel(fmt.Sprintf("channel_%02d", index)),
			TransportKind: config.MCPTransportHTTPStreamable,
		})
	}
	cfg := &config.MCPConfig{ChannelBindings: bindings}

	if _, err := newMCPServerInfoHeaderProviderForPollChannels(cfg, nil, func() bool { return false }); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("provider with possible Harpoon error = %v, want channel bound error", err)
	}
	if _, err := newMCPServerInfoHeaderProviderForPollChannels(cfg, nil, nil); err != nil {
		t.Fatalf("provider without Harpoon registry returned error: %v", err)
	}
}

func TestBuildMCPServerInfoHeaderRejectsInvalidBindings(t *testing.T) {
	t.Parallel()

	tooMany := make([]config.MCPChannelBinding, 0, mcpserverinfo.MaxChannels)
	tooMany = append(tooMany, config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
	})
	for index := 1; index < mcpserverinfo.MaxChannels; index++ {
		tooMany = append(tooMany, config.MCPChannelBinding{
			Channel:       types.Channel(fmt.Sprintf("channel_%02d", index)),
			TransportKind: config.MCPTransportHTTPStreamable,
		})
	}

	testCases := []struct {
		name           string
		cfg            config.MCPConfig
		harpoonEnabled bool
		wantErr        string
	}{
		{
			name: "duplicate canonical channel",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{
					{Channel: types.DefaultChannel, TransportKind: config.MCPTransportHTTPStreamable},
					{Channel: types.Channel(" MAIN "), TransportKind: config.MCPTransportStdio},
				},
			},
			wantErr: "duplicate channel declaration",
		},
		{
			name: "invalid channel",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.Channel("bad/channel"),
					TransportKind: config.MCPTransportHTTPStreamable,
				}},
			},
			wantErr: "invalid channel",
		},
		{
			name: "unsupported transport",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportKind("unknown"),
				}},
			},
			wantErr: "unsupported MCP transport",
		},
		{
			name:           "more than 32 channels with enabled harpoon",
			cfg:            config.MCPConfig{ChannelBindings: tooMany},
			harpoonEnabled: true,
			wantErr:        "exceeds maximum",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildMCPServerInfoHeader(&testCase.cfg, testCase.harpoonEnabled)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestRunPollerStartsEvenWhenFetcherBlocks(t *testing.T) {
	queue := make(controlplane.PolledCommandQueue, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	fetcher := &blockingFetcher{started: make(chan struct{}, 1)}
	queueAdapter := &queueAdapter{
		queue:  queue,
		logger: logger,
	}
	meterProvider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	poller, err := internal.NewPoller(queueAdapter, fetcher, logger, meterProvider.Meter("test"), time.Millisecond*50, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	app := fxtest.New(
		t,
		fx.Supply(logger),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)

	app.RequireStart()
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("poller did not start poll loop")
	}
	app.RequireStop()
}

type blockingFetcher struct {
	started chan struct{}
}

func (f *blockingFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, "", ctx.Err()
}
