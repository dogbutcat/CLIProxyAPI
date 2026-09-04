#!/bin/zsh
source ~/.zshrc >/dev/null 2>&1
setopt aliases
set -e
set -u
set -o pipefail

readonly SCRIPT_DIR=${0:A:h}
readonly GUARD="${SCRIPT_DIR}/fork_rebase_guard.zsh"
readonly TMP_ROOT=$(mktemp -d)
readonly REPO="${TMP_ROOT}/repo"

cleanup() {
	rm -rf -- "$TMP_ROOT"
}
trap cleanup EXIT

expect_pass() {
	local label=$1
	shift
	if ! "$@" >"${TMP_ROOT}/output.log" 2>&1; then
		print -u2 -- "FAIL: ${label} should pass"
		cat "${TMP_ROOT}/output.log" >&2
		exit 1
	fi
	print -- "PASS: ${label}"
}

expect_fail() {
	local label=$1
	shift
	if "$@" >"${TMP_ROOT}/output.log" 2>&1; then
		print -u2 -- "FAIL: ${label} should fail"
		cat "${TMP_ROOT}/output.log" >&2
		exit 1
	fi
	print -- "PASS: ${label} rejected"
}

git init -q "$REPO"
git -C "$REPO" config user.name ForkGuardTest
git -C "$REPO" config user.email fork-guard@example.invalid
mkdir -p "$REPO/scripts" "$REPO/sdk/oagmsg"
print -- '#!/bin/zsh' >"$REPO/build_local.sh"
print -- 'package oagmsg' >"$REPO/sdk/oagmsg/handler.go"
{
	print -- $'# surface\tmode\tpathspec'
	print -- $'local-build\tpreserve\tbuild_local.sh'
	print -- $'oagmsg\tpreserve\tsdk/oagmsg'
} >"$REPO/scripts/fork_protected_surfaces.tsv"
git -C "$REPO" add .
git -C "$REPO" commit -qm base
base=$(git -C "$REPO" rev-parse HEAD)

expect_pass "unchanged protected surfaces" zsh "$GUARD" check "$base" HEAD "$REPO"

rm "$REPO/build_local.sh"
git -C "$REPO" add -u
git -C "$REPO" commit -qm remove-build
expect_fail "removed exact surface" zsh "$GUARD" check "$base" HEAD "$REPO"

git -C "$REPO" reset --hard -q "$base"
rm "$REPO/sdk/oagmsg/handler.go"
git -C "$REPO" add -u
git -C "$REPO" commit -qm remove-oagmsg
expect_fail "removed directory member" zsh "$GUARD" check "$base" HEAD "$REPO"

git -C "$REPO" reset --hard -q "$base"
print -- 'package oagmsg // synchronized implementation' >"$REPO/sdk/oagmsg/handler.go"
git -C "$REPO" add .
git -C "$REPO" commit -qm update-oagmsg
expect_pass "modified protected implementation" zsh "$GUARD" check "$base" HEAD "$REPO"

git -C "$REPO" reset --hard -q "$base"
mkdir -p "$REPO/sdk/cliproxy/auth" "$REPO/sdk/api/handlers/claude"
{
	print -- $'# surface\tmode\tpathspec'
	print -- $'terminal-error-stop\tpreserve\tsdk/cliproxy/auth/conductor_terminal_errors.go'
} >"$REPO/scripts/fork_protected_surfaces.tsv"
print -- 'package auth; type candidateExhaustedUpstreamError struct{}' >"$REPO/sdk/cliproxy/auth/conductor_terminal_errors.go"
print -- 'package auth; func f(err error) { _ = isCandidateExhaustedUpstreamError(err) }' >"$REPO/sdk/cliproxy/auth/conductor_selection.go"
print -- 'package auth; func g(lastErr error) { _ = markCandidateExhaustedUpstreamError(lastErr) }' >"$REPO/sdk/cliproxy/auth/conductor_execution.go"
print -- 'package claude; func upstreamClaudeErrorEnvelope() {}' >"$REPO/sdk/api/handlers/claude/code_handlers.go"
git -C "$REPO" add .
git -C "$REPO" commit -qm terminal-error-stop-base
terminal_base=$(git -C "$REPO" rev-parse HEAD)
expect_pass "terminal error stop markers" zsh "$GUARD" check "$terminal_base" HEAD "$REPO"

print -- 'package claude' >"$REPO/sdk/api/handlers/claude/code_handlers.go"
git -C "$REPO" add .
git -C "$REPO" commit -qm remove-terminal-marker
expect_fail "missing terminal error stop marker" zsh "$GUARD" check "$terminal_base" HEAD "$REPO"

git -C "$REPO" reset --hard -q "$base"
mkdir -p "$REPO/internal/api" "$REPO/internal/translator/example" "$REPO/sdk/oagmsg"
print -- 'package api' >"$REPO/internal/api/handler.go"
print -- 'func runtimeCall() { sdktranslator.TranslateRequest("", "", "", nil, false) }' >>"$REPO/internal/api/handler.go"
print -- 'package api' >"$REPO/internal/api/handler_test.go"
print -- 'func testCall() { sdktranslator.TranslateRequest("", "", "", nil, false) }' >>"$REPO/internal/api/handler_test.go"
print -- 'package example' >"$REPO/internal/translator/example/compat.go"
print -- 'func compatCall() { translator.TranslateRequest("", "", "", nil, false) }' >>"$REPO/internal/translator/example/compat.go"
print -- 'package oagmsg' >"$REPO/sdk/oagmsg/comment.go"
print -- '// This mentions sdktranslator.TranslateStream for signature compatibility.' >>"$REPO/sdk/oagmsg/comment.go"
git -C "$REPO" add .
git -C "$REPO" commit -qm legacy-translator-runtime-call
expect_fail "legacy translator runtime call" zsh "$GUARD" check "$base" HEAD "$REPO"

git -C "$REPO" reset --hard -q "$base"
mkdir -p "$REPO/internal/api" "$REPO/internal/translator/example" "$REPO/sdk/oagmsg"
print -- 'package api' >"$REPO/internal/api/handler_test.go"
print -- 'func testCall() { sdktranslator.TranslateRequest("", "", "", nil, false) }' >>"$REPO/internal/api/handler_test.go"
print -- 'package example' >"$REPO/internal/translator/example/compat.go"
print -- 'func compatCall() { translator.TranslateRequest("", "", "", nil, false) }' >>"$REPO/internal/translator/example/compat.go"
print -- 'package oagmsg' >"$REPO/sdk/oagmsg/comment.go"
print -- '// This mentions sdktranslator.TranslateStream for signature compatibility.' >>"$REPO/sdk/oagmsg/comment.go"
git -C "$REPO" add .
git -C "$REPO" commit -qm allowed-translator-references
expect_pass "allowed legacy translator references" zsh "$GUARD" check "$base" HEAD "$REPO"
