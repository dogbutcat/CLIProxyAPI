#!/bin/zsh
source ~/.zshrc >/dev/null 2>&1
setopt aliases
set -e
set -u
set -o pipefail

readonly BINARY_NAME="cliproxyapi"
readonly BUILD_OUTPUT="/tmp/${BINARY_NAME}_new"
readonly CONFIG_PATH="/opt/homebrew/etc/${BINARY_NAME}.conf"
readonly BREW_BIN="/opt/homebrew/opt/${BINARY_NAME}/bin/${BINARY_NAME}"
readonly SCRIPT_DIR=${0:A:h}
readonly WEBUI_DIR="${SCRIPT_DIR}/../Cli-Proxy-API-Management-Center"
readonly WEBUI_OUTPUT="${WEBUI_DIR}/dist/index.html"
readonly STAGED_UI="/tmp/${BINARY_NAME}-management.html"
readonly LDFLAGS="-X main.DefaultConfigPath=${CONFIG_PATH}"

skip_ui=false
action="build"

for arg in "$@"; do
	case "$arg" in
		--skip-ui)
			skip_ui=true
			;;
		deploy)
			action="deploy"
			;;
		*)
			print -u2 -- "Unknown argument: $arg"
			print -u2 -- "Usage: ./build_local.sh [--skip-ui] [deploy]"
			exit 2
			;;
	esac
done

if [[ "$skip_ui" == false ]]; then
	if [[ ! -f "${WEBUI_DIR}/package.json" ]]; then
		print -u2 -- "Management Center sources not found: ${WEBUI_DIR}"
		print -u2 -- "Use --skip-ui only when intentionally verifying the backend build."
		exit 1
	fi

	print -- "==> Building Management Center WebUI"
	(
		cd "$WEBUI_DIR"
		npm run build --silent
	)
	if [[ ! -f "$WEBUI_OUTPUT" ]]; then
		print -u2 -- "WebUI build produced no dist/index.html"
		exit 1
	fi
	cp "$WEBUI_OUTPUT" "$STAGED_UI"
	print -- "==> WebUI staged: $(du -h "$STAGED_UI" | cut -f1 | xargs)"
else
	print -- "==> Skipping WebUI build (--skip-ui)"
fi

print -- "==> Building ${BINARY_NAME} with DefaultConfigPath=${CONFIG_PATH}"
go build -ldflags "$LDFLAGS" -o "$BUILD_OUTPUT" ./cmd/server
print -- "==> Build OK: ${BUILD_OUTPUT}"

if [[ "$action" != "deploy" ]]; then
	print -- ""
	print -- "To deploy, run:"
	print -- "  ./build_local.sh deploy"
	exit 0
fi

print -- "==> Deploying binary to ${BREW_BIN}"
install -m 0755 "$BUILD_OUTPUT" "$BREW_BIN"

if [[ "$skip_ui" == false ]]; then
	static_path=${MANAGEMENT_STATIC_PATH:-"$(dirname "$CONFIG_PATH")/static"}
	if [[ "${static_path:t}" == "management.html" ]]; then
		management_file="$static_path"
	else
		management_file="${static_path}/management.html"
	fi
	mkdir -p "${management_file:h}"
	install -m 0644 "$STAGED_UI" "$management_file"
	print -- "==> Management Center deployed: ${management_file}"
else
	print -- "==> Existing Management Center left unchanged"
fi

print -- "==> Restarting Homebrew service"
brew services restart "$BINARY_NAME"
sleep 2

if ! brew services info "$BINARY_NAME" 2>/dev/null | rg -q '^Running: true$'; then
	print -u2 -- "Service failed to start. Check: brew services info ${BINARY_NAME}"
	exit 1
fi

pid=$(brew services info "$BINARY_NAME" 2>/dev/null | awk '/PID/ {print $2}')
print -- "==> Service running (PID: ${pid:-unknown})"
