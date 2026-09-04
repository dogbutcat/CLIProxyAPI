package plusstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AccountActionTypeDelete = "delete"
	AccountActionTypeReauth = "reauth"
	AccountActionTypeReview = "review"

	AccountActionStatusPending  = "pending"
	AccountActionStatusIgnored  = "ignored"
	AccountActionStatusResolved = "resolved"
	AccountActionStatusDeleted  = "deleted"
)

var accountActionCandidateSchemaSQL = []string{
	`create table if not exists account_action_candidates (
		id integer primary key autoincrement,
		action_type text not null,
		status text not null,
		provider text,
		auth_file_name text not null,
		auth_index text,
		account_snapshot text,
		account_id_snapshot text,
		auth_label text,
		reason_code text,
		reason text not null,
		auto_disable_eligible integer not null default 0,
		auto_disabled_at_ms integer,
		evidence_json text,
		last_error text,
		first_seen_at_ms integer not null,
		last_seen_at_ms integer not null,
		hit_count integer not null default 1,
		created_at_ms integer not null,
		updated_at_ms integer not null
	)`,
	`create unique index if not exists idx_account_action_candidates_pending_identity_action
		on account_action_candidates(auth_file_name, action_type, coalesce(auth_index, ''), coalesce(account_id_snapshot, ''), coalesce(reason_code, '')) where status = 'pending'`,
	`create index if not exists idx_account_action_candidates_status_seen on account_action_candidates(status, last_seen_at_ms)`,
	`create table if not exists account_action_event_ledger (
		event_hash text primary key,
		candidate_id integer not null,
		created_at_ms integer not null
	)`,
}

type AccountActionCandidate struct {
	ID                  int64  `json:"id"`
	ActionType          string `json:"actionType"`
	Status              string `json:"status"`
	Provider            string `json:"provider,omitempty"`
	AuthFileName        string `json:"authFileName"`
	AuthIndex           string `json:"authIndex,omitempty"`
	AccountSnapshot     string `json:"accountSnapshot,omitempty"`
	AccountIDSnapshot   string `json:"accountIdSnapshot,omitempty"`
	AuthLabel           string `json:"authLabel,omitempty"`
	ReasonCode          string `json:"reasonCode,omitempty"`
	Reason              string `json:"reason"`
	AutoDisableEligible bool   `json:"autoDisableEligible"`
	AutoDisabledAtMS    int64  `json:"autoDisabledAtMs,omitempty"`
	EvidenceJSON        string `json:"-"`
	Evidence            any    `json:"evidence,omitempty"`
	LastError           string `json:"lastError,omitempty"`
	FirstSeenAtMS       int64  `json:"firstSeenAtMs"`
	LastSeenAtMS        int64  `json:"lastSeenAtMs"`
	HitCount            int    `json:"hitCount"`
	CreatedAtMS         int64  `json:"createdAtMs"`
	UpdatedAtMS         int64  `json:"updatedAtMs"`
}

type AccountActionCandidateUpsert struct {
	ActionType          string
	Provider            string
	AuthFileName        string
	AuthIndex           string
	AccountSnapshot     string
	AccountIDSnapshot   string
	AuthLabel           string
	ReasonCode          string
	Reason              string
	AutoDisableEligible bool
	EvidenceJSON        string
	SeenAtMS            int64
}

type accountActionQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) SyncAccountActionCandidates(ctx context.Context, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("sync account action candidates: store is nil")
	}
	if limit <= 0 || limit > 50000 {
		limit = 50000
	}
	rows, err := s.db.QueryContext(ctx, selectEventColumnsSQL()+`
		where failed = 1
			and coalesce(auth_file_snapshot, '') != ''
			and event_hash not in (select event_hash from account_action_event_ledger)
		order by timestamp_ms asc, id asc
		limit ?`, limit)
	if err != nil {
		return 0, fmt.Errorf("sync account action candidates: query failed events: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows, "sync account action candidates")
	if err != nil {
		return 0, err
	}
	synced := 0
	for _, event := range events {
		input, ok := accountActionCandidateFromEvent(event)
		if !ok {
			if err := s.markAccountActionEventSeen(ctx, event.EventHash, 0); err != nil {
				return synced, err
			}
			continue
		}
		item, err := s.UpsertAccountActionCandidate(ctx, input)
		if err != nil {
			return synced, err
		}
		if err := s.markAccountActionEventSeen(ctx, event.EventHash, item.ID); err != nil {
			return synced, err
		}
		synced++
	}
	return synced, nil
}

func (s *Store) UpsertAccountActionCandidate(ctx context.Context, input AccountActionCandidateUpsert) (AccountActionCandidate, error) {
	if s == nil || s.db == nil {
		return AccountActionCandidate{}, fmt.Errorf("upsert account action candidate: store is nil")
	}
	input.AuthFileName = strings.TrimSpace(input.AuthFileName)
	if input.AuthFileName == "" {
		return AccountActionCandidate{}, errors.New("auth file name is required")
	}
	input.ActionType = normalizeAccountActionType(input.ActionType)
	input.Provider = normalizeAccountActionProvider(input.Provider)
	input.AuthIndex = strings.TrimSpace(input.AuthIndex)
	input.AccountSnapshot = strings.TrimSpace(input.AccountSnapshot)
	input.AccountIDSnapshot = strings.TrimSpace(input.AccountIDSnapshot)
	input.AuthLabel = strings.TrimSpace(input.AuthLabel)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.Reason = strings.TrimSpace(input.Reason)
	input.EvidenceJSON = strings.TrimSpace(input.EvidenceJSON)

	now := time.Now().UnixMilli()
	seenAt := input.SeenAtMS
	if seenAt <= 0 {
		seenAt = now
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountActionCandidate{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx, `select id from account_action_candidates
		where status = ? and auth_file_name = ? and action_type = ?
			and coalesce(auth_index, '') = ? and coalesce(account_id_snapshot, '') = ?
			and coalesce(reason_code, '') = ?
		limit 1`, AccountActionStatusPending, input.AuthFileName, input.ActionType, input.AuthIndex, input.AccountIDSnapshot, input.ReasonCode).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AccountActionCandidate{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		res, execErr := tx.ExecContext(ctx, `insert into account_action_candidates (
			action_type, status, provider, auth_file_name, auth_index, account_snapshot, account_id_snapshot, auth_label,
			reason_code, reason, auto_disable_eligible, evidence_json, first_seen_at_ms, last_seen_at_ms, hit_count, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			input.ActionType,
			AccountActionStatusPending,
			nullString(input.Provider),
			input.AuthFileName,
			nullString(input.AuthIndex),
			nullString(input.AccountSnapshot),
			nullString(input.AccountIDSnapshot),
			nullString(input.AuthLabel),
			nullString(input.ReasonCode),
			nullString(input.Reason),
			boolInt(input.AutoDisableEligible),
			nullString(input.EvidenceJSON),
			seenAt,
			seenAt,
			now,
			now,
		)
		if execErr != nil {
			return AccountActionCandidate{}, execErr
		}
		id, err = res.LastInsertId()
		if err != nil {
			return AccountActionCandidate{}, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `update account_action_candidates set
			provider = coalesce(nullif(?, ''), provider),
			account_snapshot = coalesce(nullif(?, ''), account_snapshot),
			auth_label = coalesce(nullif(?, ''), auth_label),
			reason_code = coalesce(nullif(?, ''), reason_code),
			reason = coalesce(nullif(?, ''), reason),
			auto_disable_eligible = max(auto_disable_eligible, ?),
			evidence_json = coalesce(nullif(?, ''), evidence_json),
			last_error = null,
			last_seen_at_ms = ?,
			hit_count = hit_count + 1,
			updated_at_ms = ?
			where id = ?`, input.Provider, input.AccountSnapshot, input.AuthLabel, input.ReasonCode, input.Reason, boolInt(input.AutoDisableEligible), input.EvidenceJSON, seenAt, now, id)
		if err != nil {
			return AccountActionCandidate{}, err
		}
	}
	item, err := accountActionCandidateByID(ctx, tx, id)
	if err != nil {
		return AccountActionCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return AccountActionCandidate{}, err
	}
	return item, nil
}

func (s *Store) ListAccountActionCandidates(ctx context.Context, status string, limit int) ([]AccountActionCandidate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("list account action candidates: store is nil")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status = strings.TrimSpace(status)
	query := selectAccountActionCandidatesSQL
	args := []any{}
	if status != "" {
		query += ` where status = ?`
		args = append(args, status)
	}
	query += ` order by case status when 'pending' then 0 else 1 end, last_seen_at_ms desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountActionCandidate, 0)
	for rows.Next() {
		item, err := scanAccountActionCandidate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CountAccountActionCandidates(ctx context.Context, status string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("count account action candidates: store is nil")
	}
	var count int64
	status = strings.TrimSpace(status)
	if status == "" {
		if err := s.db.QueryRowContext(ctx, `select count(*) from account_action_candidates`).Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from account_action_candidates where status = ?`, status).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) GetAccountActionCandidate(ctx context.Context, id int64) (AccountActionCandidate, bool, error) {
	if s == nil || s.db == nil || id <= 0 {
		return AccountActionCandidate{}, false, nil
	}
	item, err := accountActionCandidateByID(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountActionCandidate{}, false, nil
	}
	if err != nil {
		return AccountActionCandidate{}, false, err
	}
	return item, true, nil
}

func (s *Store) UpdatePendingAccountActionCandidateStatus(ctx context.Context, id int64, status string) (AccountActionCandidate, error) {
	return s.updateAccountActionCandidateStatus(ctx, id, status, true, "")
}

func (s *Store) RecordAccountActionCandidateFailure(ctx context.Context, id int64, reason string) (AccountActionCandidate, error) {
	if s == nil || s.db == nil {
		return AccountActionCandidate{}, fmt.Errorf("record account action candidate failure: store is nil")
	}
	if id <= 0 {
		return AccountActionCandidate{}, errors.New("candidate id is required")
	}
	res, err := s.db.ExecContext(ctx, `update account_action_candidates set last_error = ?, updated_at_ms = ? where id = ?`, nullString(strings.TrimSpace(reason)), time.Now().UnixMilli(), id)
	if err != nil {
		return AccountActionCandidate{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return AccountActionCandidate{}, sql.ErrNoRows
	}
	return accountActionCandidateByID(ctx, s.db, id)
}

func (s *Store) updateAccountActionCandidateStatus(ctx context.Context, id int64, status string, pendingOnly bool, lastError string) (AccountActionCandidate, error) {
	if s == nil || s.db == nil {
		return AccountActionCandidate{}, fmt.Errorf("update account action candidate: store is nil")
	}
	if id <= 0 {
		return AccountActionCandidate{}, errors.New("candidate id is required")
	}
	status = normalizeAccountActionStatus(status)
	now := time.Now().UnixMilli()
	query := `update account_action_candidates set status = ?, last_error = ?, updated_at_ms = ? where id = ?`
	args := []any{status, nullString(lastError), now, id}
	if pendingOnly {
		query += ` and status = ?`
		args = append(args, AccountActionStatusPending)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return AccountActionCandidate{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return AccountActionCandidate{}, sql.ErrNoRows
	}
	return accountActionCandidateByID(ctx, s.db, id)
}

func (s *Store) markAccountActionEventSeen(ctx context.Context, eventHash string, candidateID int64) error {
	eventHash = strings.TrimSpace(eventHash)
	if eventHash == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `insert or ignore into account_action_event_ledger(event_hash, candidate_id, created_at_ms) values(?, ?, ?)`, eventHash, candidateID, time.Now().UnixMilli())
	return err
}

const selectAccountActionCandidatesSQL = `select id, action_type, status, provider, auth_file_name, auth_index, account_snapshot, account_id_snapshot, auth_label,
	reason_code, reason, auto_disable_eligible, auto_disabled_at_ms, evidence_json, last_error, first_seen_at_ms, last_seen_at_ms, hit_count, created_at_ms, updated_at_ms
	from account_action_candidates`

func accountActionCandidateByID(ctx context.Context, q accountActionQueryer, id int64) (AccountActionCandidate, error) {
	return scanAccountActionCandidate(q.QueryRowContext(ctx, selectAccountActionCandidatesSQL+` where id = ?`, id))
}

func scanAccountActionCandidate(row rowScanner) (AccountActionCandidate, error) {
	var item AccountActionCandidate
	var provider, authIndex, accountSnapshot, accountIDSnapshot, authLabel, reasonCode, reason, evidenceJSON, lastError sql.NullString
	var autoDisableEligible int
	var autoDisabledAtMS sql.NullInt64
	if err := row.Scan(
		&item.ID,
		&item.ActionType,
		&item.Status,
		&provider,
		&item.AuthFileName,
		&authIndex,
		&accountSnapshot,
		&accountIDSnapshot,
		&authLabel,
		&reasonCode,
		&reason,
		&autoDisableEligible,
		&autoDisabledAtMS,
		&evidenceJSON,
		&lastError,
		&item.FirstSeenAtMS,
		&item.LastSeenAtMS,
		&item.HitCount,
		&item.CreatedAtMS,
		&item.UpdatedAtMS,
	); err != nil {
		return AccountActionCandidate{}, err
	}
	item.Provider = provider.String
	item.AuthIndex = authIndex.String
	item.AccountSnapshot = accountSnapshot.String
	item.AccountIDSnapshot = accountIDSnapshot.String
	item.AuthLabel = authLabel.String
	item.ReasonCode = reasonCode.String
	item.Reason = reason.String
	item.AutoDisableEligible = autoDisableEligible != 0
	if autoDisabledAtMS.Valid {
		item.AutoDisabledAtMS = autoDisabledAtMS.Int64
	}
	item.EvidenceJSON = evidenceJSON.String
	item.LastError = lastError.String
	if item.EvidenceJSON != "" {
		var evidence any
		if err := json.Unmarshal([]byte(item.EvidenceJSON), &evidence); err == nil {
			item.Evidence = evidence
		}
	}
	return item, nil
}

func accountActionCandidateFromEvent(event Event) (AccountActionCandidateUpsert, bool) {
	if !event.Failed || strings.TrimSpace(event.AuthFileSnapshot) == "" {
		return AccountActionCandidateUpsert{}, false
	}
	decision, ok := evaluateAccountActionFailure(event)
	if !ok {
		return AccountActionCandidateUpsert{}, false
	}
	seenAtMS := event.TimestampMS
	if seenAtMS <= 0 {
		seenAtMS = time.Now().UnixMilli()
	}
	return AccountActionCandidateUpsert{
		ActionType:          decision.actionType,
		Provider:            normalizeAccountActionProvider(firstNonEmptyString(event.AuthProviderSnapshot, event.Provider)),
		AuthFileName:        strings.TrimSpace(event.AuthFileSnapshot),
		AuthIndex:           strings.TrimSpace(event.AuthIndex),
		AccountSnapshot:     firstNonEmptyString(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, event.AuthFileSnapshot),
		AccountIDSnapshot:   strings.TrimSpace(event.AuthProjectIDSnapshot),
		AuthLabel:           event.AuthLabelSnapshot,
		ReasonCode:          decision.reasonCode,
		Reason:              decision.reason,
		AutoDisableEligible: decision.autoDisableEligible,
		EvidenceJSON:        buildAccountActionEvidenceJSON(event, decision),
		SeenAtMS:            seenAtMS,
	}, true
}

type accountActionDecision struct {
	actionType          string
	reasonCode          string
	reason              string
	confidence          string
	autoDisableEligible bool
}

func evaluateAccountActionFailure(event Event) (accountActionDecision, bool) {
	code := normalizeAccountActionToken(event.HeaderErrorCode)
	kind := normalizeAccountActionToken(event.HeaderErrorKind)
	if event.ResponseMetadata != nil && event.ResponseMetadata.Errors != nil {
		code = firstNonEmptyString(code, normalizeAccountActionToken(event.ResponseMetadata.Errors.Code))
		kind = firstNonEmptyString(kind, normalizeAccountActionToken(event.ResponseMetadata.Errors.Kind))
	}
	text := strings.ToLower(strings.Join([]string{event.FailSummary, code, kind}, "\n"))
	if event.FailStatusCode == 402 && strings.Contains(text, "deactivated_workspace") {
		return accountActionDecision{AccountActionTypeDelete, "workspace_deactivated", "Workspace is deactivated; review and delete the stale auth file if appropriate", "high", true}, true
	}
	if event.FailStatusCode != 401 && event.FailStatusCode != 403 {
		return accountActionDecision{}, false
	}
	if strings.Contains(text, "account_deactivated") {
		return accountActionDecision{AccountActionTypeDelete, "account_deactivated", "Account is deactivated; review and delete the stale auth file if appropriate", "high", true}, true
	}
	if accountActionTextContainsAny(text,
		"token_revoked",
		"token_invalidated",
		"invalidated_oauth_token",
		"invalidated oauth token",
		"oauth token revoked",
		"authentication token has been invalidated",
		"token has been invalidated",
	) {
		return accountActionDecision{AccountActionTypeReauth, "token_revoked", "OAuth token was revoked or invalidated; reauthorize the account", "high", true}, true
	}
	if accountActionTextContainsAny(text,
		"invalid_token",
		"invalid or expired credentials",
		"provided authentication token is expired",
		"authentication token is expired",
		"token is expired",
		"no auth context",
		"invalid_grant",
		"auth_unavailable",
		"requires reauthorization",
		"requires re-authentication",
	) {
		return accountActionDecision{AccountActionTypeReauth, "invalid_credentials", "Credentials are invalid or expired; reauthorize the account", "high", true}, true
	}
	if kind == "authentication_error" || accountActionTextContainsAny(text, "authentication_error", "unauthorized", "forbidden", "permission_denied") {
		return accountActionDecision{AccountActionTypeReview, "authentication_review", "Authentication failure requires manual review", "medium", false}, true
	}
	return accountActionDecision{}, false
}

func buildAccountActionEvidenceJSON(event Event, decision accountActionDecision) string {
	evidence := map[string]any{
		"eventHash":           event.EventHash,
		"requestId":           event.RequestID,
		"timestamp":           event.Timestamp,
		"timestampMs":         event.TimestampMS,
		"statusCode":          event.FailStatusCode,
		"failSummary":         event.FailSummary,
		"errorCode":           event.HeaderErrorCode,
		"errorType":           event.HeaderErrorKind,
		"headerErrorKind":     event.HeaderErrorKind,
		"headerErrorCode":     event.HeaderErrorCode,
		"headerTraceId":       event.HeaderTraceID,
		"authIndex":           event.AuthIndex,
		"authFileName":        event.AuthFileSnapshot,
		"accountSnapshot":     event.AccountSnapshot,
		"accountIdSnapshot":   event.AuthProjectIDSnapshot,
		"authLabel":           event.AuthLabelSnapshot,
		"provider":            normalizeAccountActionProvider(firstNonEmptyString(event.AuthProviderSnapshot, event.Provider)),
		"model":               event.Model,
		"endpoint":            event.Endpoint,
		"actionType":          decision.actionType,
		"reasonCode":          decision.reasonCode,
		"reason":              decision.reason,
		"confidence":          decision.confidence,
		"autoDisableEligible": decision.autoDisableEligible,
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	return string(data)
}

func normalizeAccountActionType(value string) string {
	switch strings.TrimSpace(value) {
	case AccountActionTypeDelete, AccountActionTypeReauth, AccountActionTypeReview:
		return strings.TrimSpace(value)
	default:
		return AccountActionTypeReview
	}
}

func normalizeAccountActionStatus(value string) string {
	switch strings.TrimSpace(value) {
	case AccountActionStatusIgnored, AccountActionStatusResolved, AccountActionStatusDeleted:
		return strings.TrimSpace(value)
	default:
		return AccountActionStatusPending
	}
}

func normalizeAccountActionProvider(value string) string {
	normalized := normalizeAccountActionToken(value)
	switch normalized {
	case "x_ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func normalizeAccountActionToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(normalized, "-", "_")
}

func accountActionTextContainsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
