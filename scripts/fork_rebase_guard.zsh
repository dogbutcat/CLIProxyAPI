#!/bin/zsh
source ~/.zshrc >/dev/null 2>&1
setopt aliases
set -e
set -u
set -o pipefail

readonly MANIFEST_PATH="scripts/fork_protected_surfaces.tsv"

usage() {
	print -u2 -- "Usage: fork_rebase_guard.zsh check <before-ref> [after-ref] [repo]"
}

list_paths() {
	local repo=$1
	local ref=$2
	local pathspec=$3
	git -C "$repo" ls-tree -r --name-only "$ref" -- "$pathspec" | LC_ALL=C sort
}

check_surfaces() {
	local before_ref=$1
	local after_ref=$2
	local repo=$3
	local failures=0
	local rows=0
	local surface mode pathspec rest before_paths after_paths removed_path

	git -C "$repo" rev-parse --verify "${before_ref}^{commit}" >/dev/null
	git -C "$repo" rev-parse --verify "${after_ref}^{commit}" >/dev/null
	if ! git -C "$repo" cat-file -e "${before_ref}:${MANIFEST_PATH}" 2>/dev/null; then
		print -u2 -- "Protected-surface manifest is missing from before-ref: ${before_ref}:${MANIFEST_PATH}"
		return 1
	fi

	while IFS=$'\t' read -r surface mode pathspec rest; do
		[[ -z "$surface" || "$surface" == \#* ]] && continue
		(( rows += 1 ))
		if [[ -n "$rest" || -z "$pathspec" || "$mode" != (presence|preserve) ]]; then
			print -u2 -- "Invalid manifest row ${rows}: ${surface}/${mode}/${pathspec}"
			(( failures += 1 ))
			continue
		fi

		before_paths=$(list_paths "$repo" "$before_ref" "$pathspec")
		after_paths=$(list_paths "$repo" "$after_ref" "$pathspec")
		if [[ -z "$after_paths" ]]; then
			print -u2 -- "MISSING [${surface}] ${pathspec}"
			(( failures += 1 ))
			continue
		fi

		if [[ "$mode" == "preserve" && -n "$before_paths" ]]; then
			while IFS= read -r removed_path; do
				[[ -z "$removed_path" ]] && continue
				print -u2 -- "REMOVED [${surface}] ${removed_path}"
				(( failures += 1 ))
			done < <(comm -23 <(print -r -- "$before_paths") <(print -r -- "$after_paths"))
		fi
	done < <(git -C "$repo" show "${before_ref}:${MANIFEST_PATH}")

	if ! check_legacy_translator_runtime_calls "$after_ref" "$repo"; then
		(( failures += 1 ))
	fi
	if ! check_terminal_error_stop_markers "$before_ref" "$after_ref" "$repo"; then
		(( failures += 1 ))
	fi

	if (( rows == 0 )); then
		print -u2 -- "Protected-surface manifest contains no entries"
		return 1
	fi
	if (( failures > 0 )); then
		print -u2 -- "Fork rebase guard failed with ${failures} protected-surface violation(s)."
		return 1
	fi

	print -- "Fork rebase guard passed: ${rows} protected pathspec(s), ${before_ref} -> ${after_ref}"
}

check_legacy_translator_runtime_calls() {
	local after_ref=$1
	local repo=$2
	local hits

	hits=$(
		(git -C "$repo" grep -n -E '(^|[^[:alnum:]_])(sdktranslator|translator)\.Translate(Request|NonStream|Stream|TokenCount)\(' "$after_ref" -- \
			':(glob)**/*.go' \
			':(exclude)**/*_test.go' \
			':(exclude)sdk/translator/**' \
			':(exclude)internal/translator/**' 2>/dev/null || true) |
			awk -F: '
				{
					line = $0
					sub(/^[^:]+:[^:]+:[0-9]+:/, "", line)
					if (line !~ /^[[:space:]]*\/\//) {
						print $0
					}
				}
			'
	)
	if [[ -n "$hits" ]]; then
		print -u2 -- "LEGACY_TRANSLATOR_RUNTIME_CALLS detected outside compatibility/test paths:"
		print -u2 -- "$hits"
		return 1
	fi
	return 0
}

manifest_has_surface() {
	local before_ref=$1
	local repo=$2
	local surface=$3

	git -C "$repo" show "${before_ref}:${MANIFEST_PATH}" |
		awk -F '\t' -v surface="$surface" '$1 == surface { found = 1 } END { exit(found ? 0 : 1) }'
}

check_terminal_error_stop_markers() {
	local before_ref=$1
	local after_ref=$2
	local repo=$3
	local failures=0

	if ! manifest_has_surface "$before_ref" "$repo" "terminal-error-stop"; then
		return 0
	fi

	if ! git -C "$repo" grep -q 'candidateExhaustedUpstreamError' "$after_ref" -- 'sdk/cliproxy/auth/conductor_terminal_errors.go' 2>/dev/null; then
		print -u2 -- "MISSING_MARKER [terminal-error-stop] candidateExhaustedUpstreamError"
		(( failures += 1 ))
	fi
	if ! git -C "$repo" grep -q 'isCandidateExhaustedUpstreamError(err)' "$after_ref" -- 'sdk/cliproxy/auth/conductor_selection.go' 2>/dev/null; then
		print -u2 -- "MISSING_MARKER [terminal-error-stop] retry stop gate"
		(( failures += 1 ))
	fi
	if ! git -C "$repo" grep -q 'markCandidateExhaustedUpstreamError(lastErr)' "$after_ref" -- 'sdk/cliproxy/auth/conductor_execution.go' 2>/dev/null; then
		print -u2 -- "MISSING_MARKER [terminal-error-stop] exhausted sweep wrapper"
		(( failures += 1 ))
	fi
	if ! git -C "$repo" grep -q 'upstreamClaudeErrorEnvelope' "$after_ref" -- 'sdk/api/handlers/claude/code_handlers.go' 2>/dev/null; then
		print -u2 -- "MISSING_MARKER [terminal-error-stop] Claude upstream error passthrough"
		(( failures += 1 ))
	fi

	(( failures == 0 ))
}

if (( $# < 2 )) || [[ "$1" != "check" ]]; then
	usage
	exit 2
fi

before_ref=$2
after_ref=${3:-HEAD}
repo=${4:-$PWD}
check_surfaces "$before_ref" "$after_ref" "$repo"
