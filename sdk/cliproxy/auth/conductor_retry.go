package auth

import (
	"context"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	transientRetryInitialBackoff = time.Second
	transientRetryMaxBackoff     = 8 * time.Second
)

var sleepTransientRetryBackoff = sleepTransientRetryBackoffContext

// SetTransientRetryCount updates same-credential retry attempts for transient upstream errors.
// A value <= 0 disables same-credential retry and preserves immediate alternate-auth behavior.
func (m *Manager) SetTransientRetryCount(count int) {
	if m == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	m.transientRetryCount.Store(int32(count))
}

func (m *Manager) sameAuthTransientRetryCount() int {
	if m == nil {
		return 0
	}
	count := int(m.transientRetryCount.Load())
	if count < 0 {
		return 0
	}
	return count
}

// TransientRetryCount returns the active same-credential transient retry count.
func (m *Manager) TransientRetryCount() int {
	return m.sameAuthTransientRetryCount()
}

func isTransientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isSameAuthTransientRetryError(err error) bool {
	if err == nil || isRequestInvalidError(err) {
		return false
	}
	return isTransientStatus(statusCodeFromError(err))
}

func transientRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return transientRetryInitialBackoff
	}
	if attempt >= 3 {
		return transientRetryMaxBackoff
	}
	wait := transientRetryInitialBackoff << uint(attempt)
	if wait > transientRetryMaxBackoff {
		return transientRetryMaxBackoff
	}
	return wait
}

func (m *Manager) retrySameAuth(ctx context.Context, auth *Auth, err error, attempt int) (bool, error) {
	maxRetries := m.sameAuthTransientRetryCount()
	if maxRetries <= 0 || attempt < 0 || attempt >= maxRetries || !isSameAuthTransientRetryError(err) {
		return false, nil
	}
	wait := transientRetryBackoff(attempt)
	entry := logEntryWithRequestID(ctx)
	entry.WithFields(log.Fields{
		"status":  statusCodeFromError(err),
		"attempt": attempt + 1,
		"max":     maxRetries,
		"wait":    wait,
		"auth":    authIDForRetryLog(auth),
	}).Info("same-auth transient retry")
	if errWait := sleepTransientRetryBackoff(ctx, wait); errWait != nil {
		return false, errWait
	}
	return true, nil
}

func authIDForRetryLog(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func sleepTransientRetryBackoffContext(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
