package oagmsg

import (
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

// claudeToolUseIDSanitizer replaces characters not matching Claude's
// tool_use.id regex ^[a-zA-Z0-9_-]+$ with underscores.
//
// This is a local copy of internal/util.SanitizeClaudeToolID because
// sdk/oagmsg cannot import internal packages.
var (
	claudeToolUseIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeToolUseIDCounter   uint64
)

// sanitizeClaudeToolID ensures the given id conforms to Claude's
// tool_use.id regex ^[a-zA-Z0-9_-]+$.  Non-conforming characters are
// replaced with '_'; an empty result gets a generated fallback.
func sanitizeClaudeToolID(id string) string {
	s := claudeToolUseIDSanitizer.ReplaceAllString(id, "_")
	if s == "" {
		s = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&claudeToolUseIDCounter, 1))
	}
	return s
}
