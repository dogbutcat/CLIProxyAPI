package cliproxy

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	usagebridge "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/localcapture"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const localCapturePluginName = "local-capture"

type serviceUsageRuntime struct {
	mu      sync.Mutex
	bridge  *usagebridge.Bridge
	started bool
}

func (s *Service) startUsageRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.usageRuntime.mu.Lock()
	defer s.usageRuntime.mu.Unlock()
	if s.usageRuntime.started {
		return nil
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	bridgeCfg := usagebridge.BridgeConfig{}
	if s.cfg != nil {
		bridgeCfg.ImportSession = s.cfg.UsageImportSession
		if authDir := s.cfg.AuthDir; authDir != "" {
			bridgeCfg.DBPath = filepath.Join(authDir, "data", "usage.db")
		}
	}
	bridge, err := usagebridge.NewBridge(bridgeCfg)
	if err != nil {
		return fmt.Errorf("start usage runtime: initialize bridge: %w", err)
	}
	bridge.Start(ctx)
	coreusage.StartDefault(ctx)
	coreusage.RegisterNamedPlugin(localCapturePluginName, localcapture.New(bridge.Collector()))

	s.usageRuntime.bridge = bridge
	s.usageRuntime.started = true
	if bridgeCfg.DBPath != "" {
		log.WithField("db", bridgeCfg.DBPath).Info("[usage-bridge] usage analytics initialized")
	} else {
		log.Info("[usage-bridge] local usage capture initialized")
	}
	return nil
}

func (s *Service) stopUsageRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.usageRuntime.mu.Lock()
	if !s.usageRuntime.started {
		s.usageRuntime.mu.Unlock()
		return nil
	}
	bridge := s.usageRuntime.bridge
	s.usageRuntime.bridge = nil
	s.usageRuntime.started = false
	s.usageRuntime.mu.Unlock()

	coreusage.StopDefault()
	coreusage.UnregisterNamedPlugin(localCapturePluginName)
	if bridge != nil {
		if err := bridge.Close(ctx); err != nil {
			return fmt.Errorf("stop usage runtime: close bridge: %w", err)
		}
	}
	return nil
}

func (s *Service) UsageBridge() *usagebridge.Bridge {
	if s == nil {
		return nil
	}
	s.usageRuntime.mu.Lock()
	defer s.usageRuntime.mu.Unlock()
	return s.usageRuntime.bridge
}

func (s *Service) serverOptionsWithRuntimeUsageBridge() []api.ServerOption {
	if s == nil {
		return nil
	}
	opts := append([]api.ServerOption(nil), s.serverOptions...)
	if bridge := s.UsageBridge(); bridge != nil {
		opts = append(opts, api.WithUsageBridge(bridge))
	}
	return opts
}
