package fx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/controlplane/internal"
	"github.com/openai/tunnel-client/pkg/harpoon"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpserverinfo"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

// Module wires control-plane polling into the Fx graph.
var Module = fx.Module(
	"controlplane",
	fx.Provide(newMetadataState, newTunnelServiceClient, newPoller),
	fx.Invoke(runMetadataFetch, runPoller),
)

type fetcherParams struct {
	fx.In

	Config          *config.ControlPlaneConfig
	MCPConfig       *config.MCPConfig
	HarpoonRegistry *harpoon.Registry `optional:"true"`
	TLSBundle       *tlsconfig.Bundle
	Logging         *config.LoggingConfig
	Logger          *slog.Logger
	MeterProvider   *sdkmetric.MeterProvider
}

type clientResult struct {
	fx.Out

	Fetcher                        controlplane.Fetcher
	Responder                      controlplane.Responder
	ManagedCloudflareTunnelFetcher controlplane.ManagedCloudflareTunnelFetcher
	Client                         *internal.TunnelServiceClient
}

func newTunnelServiceClient(p fetcherParams) (clientResult, error) {
	if p.Config == nil {
		return clientResult{}, errors.New("controlplane: control-plane config is required")
	}
	var harpoonEnabled func() bool
	if p.HarpoonRegistry != nil {
		harpoonEnabled = func() bool {
			return p.HarpoonRegistry != nil && p.HarpoonRegistry.Count() > 0
		}
	}
	mcpServerInfoHeader, err := newMCPServerInfoHeaderProviderForPollChannels(p.MCPConfig, p.Config, harpoonEnabled)
	if err != nil {
		return clientResult{}, err
	}
	controlPlaneConfig := *p.Config
	controlPlaneConfig.MCPServerInfoHeader = mcpServerInfoHeader

	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	client, err := internal.NewTunnelServiceClient(context.Background(), &controlPlaneConfig, p.TLSBundle, logger, p.Logging, p.MeterProvider)
	if err != nil {
		return clientResult{}, err
	}
	route := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", p.Config.BaseURL, p.Config.HTTPProxy, p.Config.HTTPProxySource, os.LookupEnv)
	logFields := []any{
		slog.String("route_kind", string(route.Kind)),
		slog.String("route_name", route.Name),
		slog.String("target_host", route.TargetHostPort),
	}
	logFields = append(logFields, proxy.LogFields(route)...)
	logger.InfoContext(context.Background(), "control-plane route resolved", logFields...)

	return clientResult{
		Fetcher:                        client,
		Responder:                      client,
		ManagedCloudflareTunnelFetcher: client,
		Client:                         client,
	}, nil
}

func newMCPServerInfoHeaderProviderForPollChannels(mcpConfig *config.MCPConfig, controlPlane *config.ControlPlaneConfig, harpoonEnabled func() bool) (func() (string, error), error) {
	effectiveConfig := mcpConfigForPollChannels(mcpConfig, controlPlane)
	harpoonAllowed := controlPlane == nil || !controlPlane.PollChannelsConfigured || containsPollChannel(controlPlane.PollChannels, types.ChannelHarpoon)
	if len(effectiveConfig.ChannelBindings) > 0 {
		if _, err := buildMCPServerInfoHeader(effectiveConfig, false); err != nil {
			return nil, err
		}
	}
	// A registry can gain its first target after startup through OAuth or host
	// registration. Validate that future enabled shape before any request can
	// depend on it.
	if harpoonEnabled != nil && harpoonAllowed {
		if _, err := buildMCPServerInfoHeader(effectiveConfig, true); err != nil {
			return nil, err
		}
	}
	provider := func() (string, error) {
		enabled := harpoonAllowed && harpoonEnabled != nil && harpoonEnabled()
		return buildMCPServerInfoHeader(effectiveConfig, enabled)
	}
	return provider, nil
}

func mcpConfigForPollChannels(mcpConfig *config.MCPConfig, controlPlane *config.ControlPlaneConfig) *config.MCPConfig {
	if mcpConfig == nil || controlPlane == nil || !controlPlane.PollChannelsConfigured {
		return mcpConfig
	}
	filtered := *mcpConfig
	filtered.ChannelBindings = nil
	for _, binding := range mcpConfig.ChannelBindings {
		if containsPollChannel(controlPlane.PollChannels, binding.Channel.Canonical()) {
			filtered.ChannelBindings = append(filtered.ChannelBindings, binding)
		}
	}
	if !containsPollChannel(controlPlane.PollChannels, types.DefaultChannel) {
		filtered.AllowNoMain = true
	}
	return &filtered
}

func containsPollChannel(channels []types.Channel, want types.Channel) bool {
	for _, channel := range channels {
		if channel == want {
			return true
		}
	}
	return false
}

func buildMCPServerInfoHeader(mcpConfig *config.MCPConfig, harpoonEnabled bool) (string, error) {
	if mcpConfig == nil {
		return "", errors.New("controlplane: MCP config is required")
	}

	bindings := mcpConfig.ChannelBindings
	if len(bindings) == 0 && !mcpConfig.AllowNoMain {
		bindings = []config.MCPChannelBinding{{
			Channel:       types.DefaultChannel,
			TransportKind: mcpConfig.TransportKind,
		}}
	}

	declarations := make([]mcpserverinfo.Declaration, 0, len(bindings)+1)
	for _, binding := range bindings {
		if binding.Channel.String() == "" {
			return "", errors.New("controlplane: build MCP server info: channel name is required")
		}
		channel, err := types.NormalizeChannel(binding.Channel.String())
		if err != nil {
			return "", fmt.Errorf("controlplane: build MCP server info: %w", err)
		}
		processAffinity, err := processAffinityForTransport(binding.TransportKind)
		if err != nil {
			return "", err
		}
		declarations = append(declarations, mcpserverinfo.Declaration{
			Name:            channel.String(),
			ProcessAffinity: processAffinity,
		})
	}

	if harpoonEnabled {
		// Harpoon is routable only while its registry contains a target, and its
		// transport is always process-local in-memory state.
		declarations = append(declarations, mcpserverinfo.Declaration{
			Name:            types.ChannelHarpoon.String(),
			ProcessAffinity: true,
		})
	}
	if len(declarations) == 0 {
		return "", errors.New("controlplane: build MCP server info: at least one routable channel is required")
	}
	header, err := mcpserverinfo.BuildV1(declarations)
	if err != nil {
		return "", fmt.Errorf("controlplane: build MCP server info: %w", err)
	}
	return header, nil
}

func processAffinityForTransport(kind config.MCPTransportKind) (bool, error) {
	switch kind {
	case "", config.MCPTransportHTTPStreamable:
		return false, nil
	case config.MCPTransportStdio, config.MCPTransportInMemory:
		return true, nil
	default:
		return false, fmt.Errorf("controlplane: build MCP server info: unsupported MCP transport %q", kind)
	}
}

type pollerParams struct {
	fx.In

	Config             *config.ControlPlaneConfig
	PolledCommandQueue controlplane.PolledCommandQueue
	Fetcher            controlplane.Fetcher
	Logger             *slog.Logger
	MeterProvider      *sdkmetric.MeterProvider
}

func newPoller(p pollerParams) (internal.Poller, error) {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	if p.PolledCommandQueue == nil {
		panic("controlplane poller: dispatcher queue is nil")
	}
	queue := &queueAdapter{
		queue:  p.PolledCommandQueue,
		logger: logger,
	}
	meter := p.MeterProvider.Meter("controlplane")
	return internal.NewPoller(queue, p.Fetcher, logger, meter, p.Config.PollTimeout, p.Config.PollDeadlineGuardrail, p.Config.PollBackoffMin, p.Config.PollBackoffMax)
}

type runnerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Logger    *slog.Logger
	Poller    internal.Poller
}

type metadataParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	Client        *internal.TunnelServiceClient
	MetadataState *controlplane.MetadataState
}

func newMetadataState() *controlplane.MetadataState {
	return controlplane.NewMetadataState()
}

func runMetadataFetch(p metadataParams) error {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				metadata, err := p.Client.FetchTunnelMetadata(ctx)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					var statusErr *internal.MetadataStatusError
					if errors.As(err, &statusErr) {
						attrs := []any{
							slog.Int("status_code", statusErr.StatusCode()),
							slog.String("status", statusErr.Status()),
							slog.String("error", statusErr.Error()),
						}
						if statusErr.Code() != "" {
							attrs = append(attrs, slog.String("error_code", statusErr.Code()))
						}
						if statusErr.Message() != "" {
							attrs = append(attrs, slog.String("error_message", statusErr.Message()))
						}
						if statusErr.Mitigation() != "" {
							attrs = append(attrs, slog.String("mitigation", statusErr.Mitigation()))
						}
						logger.WarnContext(ctx, "tunnel metadata fetch failed", attrs...)
						p.MetadataState.Set(nil, err)
						return
					}
					logger.WarnContext(ctx, "tunnel metadata fetch failed", slog.String("error", err.Error()))
					p.MetadataState.Set(nil, err)
					return
				}

				p.MetadataState.Set(&controlplane.TunnelMetadata{
					ID:          metadata.ID,
					Name:        metadata.Name,
					Description: metadata.Description,
				}, nil)
				logger.InfoContext(
					ctx,
					"tunnel metadata fetched",
					slog.String("tunnel_id", metadata.ID),
					slog.String("name", metadata.Name),
					slog.String("description", metadata.Description),
				)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})

	return nil
}

func runPoller(p runnerParams) error {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.InfoContext(ctx, "starting control-plane poller")
			go func() {
				defer close(done)
				p.Poller.Run(ctx)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			logger.InfoContext(ctx, "stopping control-plane poller")
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})

	return nil
}

type queueAdapter struct {
	queue  controlplane.PolledCommandQueue
	logger *slog.Logger
}

func (q *queueAdapter) Capacity() int {
	return cap(q.queue)
}

func (q *queueAdapter) Length() int {
	return len(q.queue)
}

func (q *queueAdapter) Enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool {
	select {
	case <-ctx.Done():
		return false
	case q.queue <- cmd:
		return true
	}
}
