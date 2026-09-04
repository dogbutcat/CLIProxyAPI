package hub

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const manualQueryMaxBodyBytes = 1 << 20

const (
	manualQueryErrorMatchPanic      = "match_panic"
	manualQueryErrorAdapter         = "adapter_error"
	manualQueryErrorAdapterPanic    = "adapter_panic"
	manualQueryErrorValidation      = "validation_error"
	manualQueryErrorCompletionPanic = "completion_panic"
)

var errManualQueryBodyExpired = errors.New("quota hub: borrowed response body expired")

// ManualQueryCompletion consumes one bounded upstream response synchronously.
// A completion is single-use; calls after the first are no-ops.
type ManualQueryCompletion func(context.Context, ManualQueryResponse)

// ManualQueryResponse contains the only upstream response fields exposed to a
// manual quota adapter. Body is borrowed for the duration of the completion.
type ManualQueryResponse struct {
	StatusCode int
	ServerDate string
	Body       []byte
}

// BeginManualQuery prepares a causally ordered manual quota observation after
// matching an active endpoint and immediately before its network request.
func BeginManualQuery(
	ctx context.Context,
	manager *auth.Manager,
	resolvedAuth *auth.Auth,
	method string,
	queryURL *url.URL,
) ManualQueryCompletion {
	return beginManualQueryWithTable(ctx, manager, resolvedAuth, method, queryURL, activeManualAdapterTable())
}

func beginManualQueryWithTable(
	_ context.Context,
	manager *auth.Manager,
	resolvedAuth *auth.Auth,
	method string,
	queryURL *url.URL,
	table manualAdapterTable,
) (completion ManualQueryCompletion) {
	defer func() {
		if recover() != nil {
			completion = nil
		}
	}()

	if manager == nil || resolvedAuth == nil || resolvedAuth.Disabled || resolvedAuth.Status == auth.StatusDisabled {
		return nil
	}
	authID := resolvedAuth.ID
	provider := strings.TrimSpace(resolvedAuth.Provider)
	method = strings.ToUpper(method)
	if strings.TrimSpace(authID) == "" || provider == "" || !validManualQueryMethod(method) || !validManualQueryURL(queryURL) {
		return nil
	}

	query := manualQueryMetadata{
		Provider:    provider,
		Method:      method,
		Scheme:      queryURL.Scheme,
		Host:        queryURL.Host,
		Hostname:    queryURL.Hostname(),
		Port:        queryURL.Port(),
		Path:        queryURL.Path,
		EscapedPath: queryURL.EscapedPath(),
		RawQuery:    queryURL.RawQuery,
	}
	endpoint := sanitizedManualQueryEndpoint(query)
	adapter, matched, matchPanicked := matchManualQueryAdapter(table, query)
	if matchPanicked {
		logManualQueryFailure(provider, endpoint, manualQueryErrorMatchPanic)
		return nil
	}
	if !matched {
		return nil
	}

	ticket, issued := manager.IssueQuotaObservationTicketForAuth(resolvedAuth)
	if !issued {
		return nil
	}
	attempt := &manualQueryAttempt{
		manager:  manager,
		adapter:  adapter,
		authID:   ticket.AuthID,
		provider: ticket.Provider,
		ticket:   ticket,
		endpoint: endpoint,
	}
	return attempt.complete
}

type manualQueryAttempt struct {
	once     sync.Once
	manager  *auth.Manager
	adapter  manualQueryAdapter
	authID   string
	provider string
	ticket   auth.QuotaObservationTicket
	endpoint string
}

func (attempt *manualQueryAttempt) complete(ctx context.Context, response ManualQueryResponse) {
	defer func() {
		if recover() != nil {
			logManualQueryFailure(attempt.provider, attempt.endpoint, manualQueryErrorCompletionPanic)
		}
	}()
	attempt.once.Do(func() {
		attempt.consume(ctx, response)
	})
}

func (attempt *manualQueryAttempt) consume(ctx context.Context, response ManualQueryResponse) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices ||
		len(response.Body) > manualQueryMaxBodyBytes {
		return
	}

	completedAt := time.Now().UTC()
	var serverDate time.Time
	if parsed, err := http.ParseTime(strings.TrimSpace(response.ServerDate)); err == nil {
		serverDate = parsed.UTC()
	}
	responseBody := newBorrowedManualBody(response.Body)
	observation, err, observePanicked := observeManualQueryAdapter(attempt.adapter, manualResponseMetadata{
		CompletedAt: completedAt,
		ServerDate:  serverDate,
	}, responseBody)
	if observePanicked {
		logManualQueryFailure(attempt.provider, attempt.endpoint, manualQueryErrorAdapterPanic)
		return
	}
	if err != nil {
		logManualQueryFailure(attempt.provider, attempt.endpoint, manualQueryErrorAdapter)
		return
	}
	batch, err := (ObservationBatch{
		AuthID:   attempt.authID,
		Provider: attempt.provider,
		Ticket:   attempt.ticket,
		Metadata: ObservationMetadata{
			Source:      ManagementManual,
			CompletedAt: completedAt,
			ServerDate:  serverDate,
		},
		Observation: observation,
	}).ToAuthBatch()
	if err != nil {
		logManualQueryFailure(attempt.provider, attempt.endpoint, manualQueryErrorValidation)
		return
	}
	applyContext := context.Background()
	if ctx != nil {
		applyContext = context.WithoutCancel(ctx)
	}
	attempt.manager.ApplyQuotaObservationBatch(applyContext, batch)
}

type borrowedManualBody struct {
	mu      sync.Mutex
	body    []byte
	offset  int
	expired bool
}

func newBorrowedManualBody(body []byte) *borrowedManualBody {
	return &borrowedManualBody{body: body}
}

func (body *borrowedManualBody) Read(buffer []byte) (int, error) {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.expired {
		return 0, errManualQueryBodyExpired
	}
	if body.offset >= len(body.body) {
		return 0, io.EOF
	}
	read := copy(buffer, body.body[body.offset:])
	body.offset += read
	return read, nil
}

func (body *borrowedManualBody) expire() {
	body.mu.Lock()
	body.body = nil
	body.offset = 0
	body.expired = true
	body.mu.Unlock()
}

func observeManualQueryAdapter(
	adapter manualQueryAdapter,
	metadata manualResponseMetadata,
	body *borrowedManualBody,
) (observation Observation, err error, panicked bool) {
	defer func() {
		body.expire()
		if recover() != nil {
			observation = Observation{}
			err = nil
			panicked = true
		}
	}()
	observation, err = adapter.observeResponse(metadata, body)
	return observation, err, false
}

func matchManualQueryAdapter(table manualAdapterTable, query manualQueryMetadata) (adapter manualQueryAdapter, matched, panicked bool) {
	defer func() {
		if recover() != nil {
			adapter = manualQueryAdapter{}
			matched = false
			panicked = true
		}
	}()
	adapter, matched = table.match(query)
	return adapter, matched, false
}

func sanitizedManualQueryEndpoint(query manualQueryMetadata) string {
	host := strings.ToLower(query.Hostname)
	if query.Port != "" {
		host = net.JoinHostPort(host, query.Port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := query.EscapedPath
	if path == "" {
		path = query.Path
	}
	return strings.ToLower(query.Scheme) + "://" + host + path
}

func logManualQueryFailure(provider, endpoint, errorClass string) {
	defer func() {
		_ = recover()
	}()
	log.WithFields(log.Fields{
		"provider":    provider,
		"endpoint":    endpoint,
		"error_class": errorClass,
	}).Warn("quota hub: manual query synchronization failed")
}

func validManualQueryMethod(method string) bool {
	if method == "" || method != strings.TrimSpace(method) {
		return false
	}
	for index := 0; index < len(method); index++ {
		character := method[index]
		if character <= ' ' || character >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}\t", rune(character)) {
			return false
		}
	}
	return true
}

func validManualQueryURL(queryURL *url.URL) bool {
	return queryURL != nil && queryURL.Scheme == "https" && queryURL.Host != "" &&
		queryURL.Hostname() != "" && queryURL.User == nil && queryURL.Opaque == ""
}
