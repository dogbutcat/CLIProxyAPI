package auth

import "net/http"

// candidateExhaustedUpstreamError marks a request attempt where every selected
// candidate has already returned an upstream HTTP error. The wrapper prevents
// outer request retry from resetting the per-attempt tried set while preserving
// the original upstream error for callers and stream bootstrap handling.
type candidateExhaustedUpstreamError struct {
	cause error
}

func (e *candidateExhaustedUpstreamError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *candidateExhaustedUpstreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func markCandidateExhaustedUpstreamError(err error) error {
	if !shouldStopAfterCandidateExhaustion(err) || isCandidateExhaustedUpstreamError(err) {
		return err
	}
	return &candidateExhaustedUpstreamError{cause: err}
}

func unwrapCandidateExhaustedUpstreamError(err error) error {
	if marker, ok := asCandidateExhaustedUpstreamError(err); ok && marker.cause != nil {
		return marker.cause
	}
	return err
}

func isCandidateExhaustedUpstreamError(err error) bool {
	_, ok := asCandidateExhaustedUpstreamError(err)
	return ok
}

func asCandidateExhaustedUpstreamError(err error) (*candidateExhaustedUpstreamError, bool) {
	if err == nil {
		return nil, false
	}
	current := err
	for current != nil {
		if marker, ok := current.(*candidateExhaustedUpstreamError); ok {
			return marker, true
		}
		type unwrapper interface {
			Unwrap() error
		}
		wrapped, ok := current.(unwrapper)
		if !ok {
			break
		}
		current = wrapped.Unwrap()
	}
	return nil, false
}

func shouldStopAfterCandidateExhaustion(err error) bool {
	status := statusCodeFromError(err)
	return status == http.StatusTooManyRequests
}
