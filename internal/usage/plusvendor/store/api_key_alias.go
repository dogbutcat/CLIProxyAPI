package plusstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) LoadAPIKeyAliases(ctx context.Context) ([]APIKeyAlias, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("load api key aliases: store is nil")
	}
	rows, err := s.db.QueryContext(ctx, `select api_key_hash, alias, updated_at_ms from api_key_aliases order by alias, api_key_hash`)
	if err != nil {
		return nil, fmt.Errorf("load api key aliases: query: %w", err)
	}
	defer rows.Close()
	out := []APIKeyAlias{}
	for rows.Next() {
		var item APIKeyAlias
		if err := rows.Scan(&item.APIKeyHash, &item.Alias, &item.UpdatedAtMS); err != nil {
			return nil, fmt.Errorf("load api key aliases: scan: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load api key aliases: read rows: %w", err)
	}
	return out, nil
}

func (s *Store) SaveAPIKeyAliases(ctx context.Context, aliases []APIKeyAlias, activeHashes []string, cleanupOrphans bool) ([]APIKeyAlias, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("save api key aliases: store is nil")
	}
	normalized, active, err := normalizeAPIKeyAliases(aliases, activeHashes)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("save api key aliases: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if cleanupOrphans && len(active) > 0 {
		if _, err := tx.ExecContext(ctx, `delete from api_key_aliases`); err != nil {
			return nil, fmt.Errorf("save api key aliases: cleanup existing: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `delete from api_key_aliases where api_key_hash in (`+aliasPlaceholders(len(normalized))+`)`, aliasHashArgs(normalized)...); err != nil && len(normalized) > 0 {
			return nil, fmt.Errorf("save api key aliases: delete replaced aliases: %w", err)
		}
	}
	stmt, err := tx.PrepareContext(ctx, `insert into api_key_aliases(api_key_hash, alias, updated_at_ms)
		values(?,?,?)
		on conflict(api_key_hash) do update set alias=excluded.alias, updated_at_ms=excluded.updated_at_ms`)
	if err != nil {
		return nil, fmt.Errorf("save api key aliases: prepare upsert: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UnixMilli()
	for i := range normalized {
		if len(active) > 0 && !active[normalized[i].APIKeyHash] {
			continue
		}
		normalized[i].UpdatedAtMS = now
		if _, err := stmt.ExecContext(ctx, normalized[i].APIKeyHash, normalized[i].Alias, normalized[i].UpdatedAtMS); err != nil {
			return nil, fmt.Errorf("save api key aliases: upsert %s: %w", normalized[i].APIKeyHash, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("save api key aliases: commit: %w", err)
	}
	return s.LoadAPIKeyAliases(ctx)
}

func (s *Store) DeleteAPIKeyAlias(ctx context.Context, apiKeyHash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("delete api key alias: store is nil")
	}
	apiKeyHash = strings.TrimSpace(apiKeyHash)
	if apiKeyHash == "" {
		return errors.New("apiKeyHash is required")
	}
	if _, err := s.db.ExecContext(ctx, `delete from api_key_aliases where api_key_hash = ?`, apiKeyHash); err != nil {
		return fmt.Errorf("delete api key alias %s: %w", apiKeyHash, err)
	}
	return nil
}

func normalizeAPIKeyAliases(aliases []APIKeyAlias, activeHashes []string) ([]APIKeyAlias, map[string]bool, error) {
	seenHash := map[string]bool{}
	seenAlias := map[string]bool{}
	out := make([]APIKeyAlias, 0, len(aliases))
	for _, item := range aliases {
		hash := strings.TrimSpace(item.APIKeyHash)
		alias := strings.TrimSpace(item.Alias)
		if hash == "" || alias == "" {
			return nil, nil, errors.New("apiKeyHash and alias are required")
		}
		if seenHash[hash] {
			return nil, nil, fmt.Errorf("duplicate apiKeyHash %s", hash)
		}
		aliasKey := strings.ToLower(alias)
		if seenAlias[aliasKey] {
			return nil, nil, fmt.Errorf("duplicate alias %s", alias)
		}
		seenHash[hash] = true
		seenAlias[aliasKey] = true
		out = append(out, APIKeyAlias{APIKeyHash: hash, Alias: alias})
	}
	active := map[string]bool{}
	for _, value := range activeHashes {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			active[trimmed] = true
		}
	}
	return out, active, nil
}

func aliasPlaceholders(count int) string {
	if count <= 0 {
		return "''"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func aliasHashArgs(aliases []APIKeyAlias) []any {
	out := make([]any, 0, len(aliases))
	for _, item := range aliases {
		out = append(out, item.APIKeyHash)
	}
	return out
}
