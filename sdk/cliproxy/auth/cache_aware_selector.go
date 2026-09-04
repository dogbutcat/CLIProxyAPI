package auth

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

// CacheAwareSelector decorates another selector with persistent prefix-cache affinity.
type CacheAwareSelector struct {
	fallback Selector
	store    *PrefixHashStore

	mu     sync.Mutex
	closed bool
}

// CacheAwareSelectorConfig configures a cache-aware selector.
type CacheAwareSelectorConfig struct {
	Fallback Selector
	Store    *PrefixHashStore
}

// NewCacheAwareSelector creates a cache-aware selector decorator.
func NewCacheAwareSelector(fallback Selector, store *PrefixHashStore) *CacheAwareSelector {
	return NewCacheAwareSelectorWithConfig(CacheAwareSelectorConfig{Fallback: fallback, Store: store})
}

// NewCacheAwareSelectorWithConfig creates a cache-aware selector decorator.
func NewCacheAwareSelectorWithConfig(cfg CacheAwareSelectorConfig) *CacheAwareSelector {
	if cfg.Fallback == nil {
		cfg.Fallback = &SeqRandomStartSelector{}
	}
	return &CacheAwareSelector{fallback: cfg.Fallback, store: cfg.Store}
}

// Pick selects a cached eligible auth when possible, otherwise delegates to fallback.
func (s *CacheAwareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if s == nil || s.fallback == nil {
		return nil, &Error{Code: "auth_not_found", Message: "selector not configured"}
	}
	if s.isClosed() || s.store == nil || len(opts.OriginalRequest) == 0 {
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}

	fp := s.store.Fingerprint(cacheFingerprintFormat(opts), opts.OriginalRequest)
	if auth := s.lookupEligible(fp.FingerprintID, model, auths, "fingerprint"); auth != nil {
		s.registerCallbacks(opts.Metadata, fp, model, auth.ID)
		return auth, nil
	}
	if auth := s.lookupEligible(fp.CacheID, model, auths, "prefix"); auth != nil {
		s.registerCallbacks(opts.Metadata, fp, model, auth.ID)
		return auth, nil
	}
	if auth := s.lookupEligible(fp.TailID, model, auths, "tail"); auth != nil {
		s.registerCallbacks(opts.Metadata, fp, model, auth.ID)
		return auth, nil
	}

	selected, errPick := s.fallback.Pick(ctx, provider, model, opts, auths)
	if errPick != nil || selected == nil {
		return selected, errPick
	}
	s.registerCallbacks(opts.Metadata, fp, model, selected.ID)
	return selected, nil
}

// RecordSuccessfulResponse records affinity after a successful response event.
func (s *CacheAwareSelector) RecordSuccessfulResponse(originalPayload []byte, model, authID string) {
	if s == nil || s.store == nil || s.isClosed() || len(originalPayload) == 0 {
		return
	}
	fp := s.store.Fingerprint(oagmsg.FormatOpenAI, originalPayload)
	s.appendSuccessFingerprint(fp, model, authID)
}

// RecordTruncation records affinity after a successful truncation event.
func (s *CacheAwareSelector) RecordTruncation(truncatedPayload []byte, model, authID string) {
	if s == nil || s.store == nil || s.isClosed() || len(truncatedPayload) == 0 {
		return
	}
	fp := s.store.Fingerprint(oagmsg.FormatOpenAI, truncatedPayload)
	if fp.FingerprintID != "" {
		s.store.Append(fp.FingerprintID, model, authID)
	}
	if fp.CacheID != "" {
		s.store.Append(fp.CacheID, model, authID)
	}
	if fp.TailID != "" {
		s.store.Append(fp.TailID, model, authID)
	}
}

// Stop closes the underlying store and fallback selector when supported.
func (s *CacheAwareSelector) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	store := s.store
	fallback := s.fallback
	s.mu.Unlock()

	if store != nil {
		if errClose := store.Close(); errClose != nil {
			log.Warnf("cache-aware selector: close prefix store: %v", errClose)
		}
	}
	if stoppable, ok := fallback.(StoppableSelector); ok && stoppable != nil {
		stoppable.Stop()
	}
}

func (s *CacheAwareSelector) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	return closed
}

func (s *CacheAwareSelector) lookupEligible(cacheID, model string, auths []*Auth, kind string) *Auth {
	cacheID = strings.TrimSpace(cacheID)
	if cacheID == "" || s.store == nil {
		return nil
	}
	authID := s.store.Lookup(cacheID, model)
	if authID == "" {
		return nil
	}
	for _, candidate := range auths {
		if candidate != nil && candidate.ID == authID {
			log.Debugf("cache-aware selector: %s hit model=%s auth=%s", kind, model, filepath.Base(authID))
			return candidate
		}
	}
	log.Debugf("cache-aware selector: %s hit unavailable auth=%s model=%s", kind, filepath.Base(authID), model)
	return nil
}

func (s *CacheAwareSelector) registerCallbacks(meta map[string]any, fp PrefixFingerprint, model, authID string) {
	if meta == nil || authID == "" || s.store == nil || s.isClosed() {
		return
	}
	meta[CacheAwareResponseCallbackMetadataKey] = CacheAwareResponseCallback(func() {
		s.appendSuccessFingerprint(fp, model, authID)
	})
	meta[CacheAwareTruncationCallbackMetadataKey] = CacheAwareTruncationCallback(func(payload []byte) {
		s.RecordTruncation(payload, model, authID)
	})
}

func (s *CacheAwareSelector) appendSuccessFingerprint(fp PrefixFingerprint, model, authID string) {
	if s == nil || s.store == nil || s.isClosed() || strings.TrimSpace(authID) == "" {
		return
	}
	if fp.FingerprintID != "" {
		s.store.Append(fp.FingerprintID, model, authID)
	}
	if fp.CacheIDAfterSuccess != "" {
		s.store.Append(fp.CacheIDAfterSuccess, model, authID)
	}
}

func cacheFingerprintFormat(opts cliproxyexecutor.Options) oagmsg.Format {
	format := oagmsg.Format(opts.SourceFormat.String())
	if format == "" {
		return oagmsg.FormatOpenAI
	}
	return format
}
