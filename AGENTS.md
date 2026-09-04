# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
./build_local.sh # Build with WebUI
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
./build_local.sh --skip-ui # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `sdk/oagmsg/` — Fork-owned canonical protocol model and sole runtime conversion authority
- `internal/translator/` — Upstream legacy conversion sources retained for sync/reference and compatibility; not the fork's runtime conversion authority
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- During multi-upstream and CPA-Manager-Plus migrations, minimize source churn and prefer narrow protocol-preserving patches over broad rewrites unless the broader change is required.
- Before any rebase, reset, merge reconstruction, or branch replacement involving this fork, create a backup ref for the current branch tip and a Git snapshot ref that includes tracked worktree changes. Never use a rewritten migration checkpoint as the new fork baseline without comparing it to the previous fork tip.
- After rewriting history, run the fork-owned guard from the pre-rewrite ref so deletion of the guard itself cannot bypass verification: `git show <pre-ref>:scripts/fork_rebase_guard.zsh | zsh /dev/stdin check <pre-ref> HEAD "$PWD"`. Any `MISSING` or `REMOVED` result blocks completion until the owner reviews the change and updates the protected-surface manifest intentionally.
- Keep `build_local.sh`, `scripts/fork_protected_surfaces.tsv`, `scripts/fork_rebase_guard.zsh`, and its self-test as fork-exclusive infrastructure. `cpa/main` not containing these files is expected and is never grounds for deleting them.
- Migration omission checklist: before handoff, explicitly audit restored Plus surfaces for usage statistics, request monitoring, OpenCode Go provider support, `seq-random` routing, `vision-proxy` interception, Account Actions, quota/provider metadata, and dashboard navigation. These surfaces are independent and must not be treated as incidental UI polish.
- Usage monitoring and analytics client-key identity must stay consistent across aggregation, filtering, events, and UI drilldowns. Treat the client-key identity as `api_key_hash` with fallback to `source_hash` and then `source`; never filter or match Usage Analytics / Monitoring client keys against `api_key_hash` alone.
- `/v0/management/usage/monitoring/analytics` is the shared contract for Monitoring and Usage Analytics. When adding include flags used by the frontend, implement both request parsing and response payloads; `include.timeline` must return bucketed points for filtered client-key trend charts.
- `/v0/management/usage/dashboard/summary` is the shared Dashboard contract. Preserve `today`, `traffic_timeline`, `today_request_health_timeline`, and `provider_activity` so Dashboard headline traffic, success rate, chart legend, and provider activity stay aligned with Monitoring/Usage Analytics instead of legacy `recentRequests` counters.
- Usage Analytics tabs depend on the POST analytics include contract, not only on summary totals. Preserve response payloads for `model_stats`, `model_share`, `channel_share`, `credential_stats`, `credential_timeline`, `heatmap`, `anomaly_points`, `failure_sources`, `events_page`, and `drilldown_preview`; if summary totals are non-zero, these included surfaces must not silently disappear because an include flag is ignored.
- Usage Analytics and Monitoring cross-page navigation depends on stable backend filter semantics. A request built from Usage Analytics `View request details` must return the same event population when opened in Monitoring, including summary, timeline, events page, account stats, and API key stats. Do not make filters whose behavior differs between aggregate stats and event pages.
- Client-key, account, provider, model, status, source hash, auth index, and time-window filters must compose identically for totals, dimension stats, timeline, selectors, and events. If a filter narrows `MonitoringAnalytics.Totals`, it must narrow `MonitoringEventsPage` and all included stats in the same way.
- For Usage Analytics regressions, verify a client key whose events have empty `api_key_hash` but populated `source_hash`. The expected behavior is non-empty summary, timeline, events, and related execution context data for the same selected key.
- Integrated local sqlite usage must expose `account_actions` as supported (`local-sqlite-v1`). Do not revert the Management Account Actions surface to `unsupported_local_sqlite`; derive local candidates from failed `usage_events` and ledger processed `event_hash` values to avoid repeated candidate creation.
- Account Actions sqlite migrations must be compatible with already-created local tables. Keep idempotent column backfills before creating indexes that reference newer columns such as `reason_code`.
- Account Actions enable/delete mutations must not fake auth-file changes from the integrated usage handler. If direct mutation is unavailable, return a conflict and record `last_error` without changing the candidate status; wire real mutations only through management auth-file ownership checks.
- OpenCode Go provider support is a protected migration surface: keep its Anthropic-compatible `x-api-key` auth, Qwen model reachability, quota metadata, and provider-specific request behavior distinct from generic Claude-compatible/Bearer endpoints.
- `vision-proxy` is a protected migration surface: keep accepting the legacy public `vision-proxy:` config block, normalize it into `VisionConfig`, and preserve image interception for routes that require image-to-text conversion.
- OpenCode Go and `vision-proxy` must both be included in smoke validation, but they are separate omission checks: text provider reachability and image interception should be verified independently.
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.

## Fork Appendix: Protected Features

This repository is a personal fork that deliberately extends `cpa/main`. The inventory below is a minimum protection list, not permission to remove an unlisted fork delta. Before dropping any behavior during an upstream sync or rebase, prove that upstream now provides an equivalent contract and retain a regression test for it.

### Upstream Sync Boundary

- Preserve upstream executor behavior for authentication, credential selection, request policy, transport, errors, usage reporting, and newly added provider capabilities. Keep fork changes narrow and explicit at owned extension boundaries.
- "Synced with upstream" means observable upstream behavior and capabilities have been absorbed; fork-owned files need not be byte-for-byte identical. Identical protocol-conversion call sites are a reason to check whether OAGMsg ownership was lost.
- Classify every fork/upstream delta before resolving conflicts. Never accept an upstream side wholesale when it deletes a protected capability, configuration field, management route, persistence contract, provider identity, or test oracle.
- Do not create issues, pull requests, comments, releases, pushes, or any other remote mutation against `router-for-me/CLIProxyAPI` or another repository unless the user explicitly requests that exact operation and target. Read-only fetch and comparison do not imply permission to modify upstream.

### OAGMsg Protocol Aggregation

- `sdk/oagmsg` is the single conversion authority for every protocol supported by the repository. Every supported format must be accepted as an input and emitted as an output through OAGMsg, including request parsing, canonical normalization, request serialization, non-stream responses, streaming events, tool calls, thinking/signature carriers, token-count responses, web search, and custom tool content.
- Executors remain provider transport implementations. Route protocol parse/stringify and cross-format conversion through OAGMsg while preserving provider-specific wire behavior in the executor.
- Do not restore direct `sdk/translator` or `internal/translator` runtime conversion calls during a rebase. Compatibility format types, registration adapters, and plugin bridges may remain, but native protocol parsing/serialization must stay in OAGMsg and compatibility facades must delegate to it.
- When upstream changes protocol semantics, port the semantic delta into OAGMsg's canonical model and handlers, add/update parity oracles, then retain only narrow executor handoffs. A legacy translator fallback is not valid coverage.
- Preserve all supported protocol directions, including OpenAI Chat Completions, OpenAI Responses, Codex Responses, Anthropic Messages, Gemini, Antigravity, Google Interactions/Steps, xAI Responses adaptations, and plugin-provided formats registered through the supported bridge.

### Usage, Monitoring, and Management Data

- Preserve integrated local usage capture and the durable SQLite pipeline: event identity/deduplication, provider/account/model attribution, response-header metadata, failure details, model pricing, hourly/account/dashboard rollups, backfill/checkpoints, migrations, and restart-safe persistence.
- Shutdown ordering must stop intake, drain queued usage records, flush/close workers, and only then close the store. Successful requests immediately preceding shutdown must survive restart; do not regress to in-memory-only statistics.
- Preserve the Management contracts for dashboard summary, monitoring realtime/selectors/analytics/events, Usage Analytics include payloads, model-price CRUD/sync/summary, status/capabilities, imports, resumable chunked import sessions, and Account Actions.
- Keep filter composition identical across totals, timelines, selectors, dimension stats, drilldowns, and event pages. Client-key identity remains `api_key_hash`, then `source_hash`, then `source`; provider attribution must preserve the facade provider such as `opencode-go`, not only the delegated executor.
- Local SQLite Account Actions remain supported through failed-event candidates and the processed-event ledger. Migrations must remain idempotent for existing databases, and mutations must not claim an auth-file change unless the real auth owner performed it.

### Multi-Protocol Providers and OpenCode Go

- Preserve provider aggregation in which one logical provider/key group can expose multiple upstream protocols while retaining one provider identity for routing, models, quota, monitoring, and usage. Protocol-specific transport may delegate to specialized executors, but the facade identity and configuration boundary must survive.
- OpenCode Go key groups must retain OpenAI-compatible and Anthropic-compatible protocol blocks, shared keys, model aliases/prefixes/priorities, protocol/model disambiguation, `x-api-key` behavior, per-key proxy/workspace/cookie metadata, custom headers, cooling controls, and legacy config normalization.
- Preserve OpenCode Go synthesized auth lifecycle, hot reload, executor registration, model listing/routing, request/response model remapping, truncation, OpenAI cache control, vision preprocessing, quota polling/thresholds, referral and management endpoints, quota metadata, and usage provider attribution.
- Validate both OpenCode Go protocol branches independently. A working OpenAI-compatible branch does not prove the Anthropic/Qwen branch, quota path, management path, or facade attribution works.

### Routing and Credential Selection

- Preserve `seq-random` and its aliases (`sequential-random`, `seqrandom`, `sr`) across YAML parsing, management updates, normalization, persistence, SDK configuration, and live selector replacement.
- Preserve the selector's per-provider-pool sequence state: choose one random start (optionally quota-weighted) when a pool is first seeded, then loop sequentially without re-randomizing on later requests. Keep stable continuation when the eligible set changes, priority filtering, and exclusion of disabled, cooled-down, model-unavailable, or quota-exhausted credentials.
- `seq-random` is fork-exclusive until the target `cpa/main` demonstrably supplies an equivalent state machine. Do not treat an upstream scheduler refactor as a replacement merely because it applies cleanly; port the two-state invariant explicitly: `unseeded -> choose eligible start -> seeded`, then `seeded -> next eligible stable-ID successor -> seeded`.
- Scheduler fast-path and legacy/route-aware selection must obey the same sequence contract. Quota scores may bias the initial seed only; they must not trigger per-request re-randomization, and missing quota scores must not make the first selection default to index zero.
- Keep independent regression coverage for random initial distribution, sequential continuation, wraparound, changing eligible sets, exhausted-account exclusion, quota-weighted initial seeding, manager/scheduler selection, and concurrent access. Never rewrite these tests to accept repeated quota-weighted random selection as `seq-random` behavior.
- Preserve weighted credential routing, transactional config application, universal session affinity/failover behavior, and cache-aware retry routing. Cache-aware routing must continue using OAGMsg conversation fingerprints, persist successful/truncated prefix affinity, and fall back safely when the bound credential is unavailable.
- Routing tests must cover repeated sequence progression, pool changes, quota weighting, unavailable credentials, aliases/config round trips, concurrent selection, and hot-reload behavior; a one-request smoke test is insufficient.

### Vision and Provider-Specific Extensions

- Preserve the legacy public `vision-proxy:` configuration, normalization into `VisionConfig`, model capability detection, image resolution/interception, and image-to-text request rewriting. Test vision interception separately from OpenCode Go text routing.
- Preserve fork-added provider behavior that is easy to lose in upstream merges: Claude nested API-key transport and auth-header exclusivity, OpenAI-compatible prompt-cache propagation, Kimi thinking replay continuity, structured Responses tool output, Codex final-wire/cloaking constraints, Antigravity search credential/grounding behavior, and environment-proxy support for management API calls.
- Plugin hooks and executors must keep stable registration, hot-reload, and shutdown ownership. Plugin protocol hooks integrate through OAGMsg rather than reintroducing a second native conversion authority.

### Rebase Verification Gate

- Compare against the exact target `cpa/main` commit and record which upstream executor/protocol changes were absorbed into fork-owned surfaces.
- Audit production paths for direct legacy `TranslateRequest`, `TranslateNonStream`, `TranslateStream`, and `TranslateTokenCount` calls; run OAGMsg architecture/conformance/oracle tests and affected executor tests.
- Run focused persistence tests for usage shutdown/restart, migration from an existing database, analytics filter parity, dashboard/monitoring contracts, imports, and Account Actions.
- Run black-box smoke coverage for every inbound protocol against representative outbound providers, both stream and non-stream; exercise both OpenCode Go protocol branches, provider attribution, `seq-random`, cache-aware routing, and `vision-proxy` independently.
- A build-only pass is not sufficient. Do not declare a rebase or deployment complete until protected feature tests pass and the deployed binary has been exercised through its public API.
