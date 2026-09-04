package plusstore

type ModelPrice struct {
	Prompt        float64 `json:"prompt"`
	Completion    float64 `json:"completion"`
	Cache         float64 `json:"cache"`
	CacheRead     float64 `json:"cacheRead,omitempty"`
	CacheCreation float64 `json:"cacheCreation,omitempty"`
	Source        string  `json:"source,omitempty"`
	SourceModelID string  `json:"sourceModelId,omitempty"`
	RawJSON       string  `json:"rawJson,omitempty"`
	UpdatedAtMS   int64   `json:"updatedAtMs,omitempty"`
	SyncedAtMS    *int64  `json:"syncedAtMs,omitempty"`
}

type Tokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheTokens         int64 `json:"cache_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type InsertResult struct {
	Inserted            int      `json:"inserted"`
	Skipped             int      `json:"skipped"`
	InsertedEventHashes []string `json:"-"`
}

type ModelPriceSyncResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

type ModelPriceUsageStat struct {
	Model          string `json:"model"`
	Calls          int64  `json:"calls"`
	RequestedCalls int64  `json:"requested_calls"`
	ResolvedCalls  int64  `json:"resolved_calls"`
}

type ModelPriceUsageSummary struct {
	SampledEvents int64                 `json:"sampled_events"`
	TotalEvents   int64                 `json:"total_events"`
	Truncated     bool                  `json:"truncated"`
	Models        []ModelPriceUsageStat `json:"models"`
}

type APIKeyAlias struct {
	APIKeyHash  string `json:"apiKeyHash"`
	Alias       string `json:"alias"`
	UpdatedAtMS int64  `json:"updatedAtMs,omitempty"`
}

type UsageImportResult struct {
	Format      string   `json:"format,omitempty"`
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Total       int      `json:"total"`
	Failed      int      `json:"failed"`
	Unsupported int      `json:"unsupported,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}
