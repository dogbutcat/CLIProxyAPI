package api

import usagebridge "github.com/router-for-me/CLIProxyAPI/v8/internal/usage"

func (s *Server) UsageBridge() *usagebridge.Bridge {
	if s == nil {
		return nil
	}
	return s.usageBridge
}
