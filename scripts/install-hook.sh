#!/usr/bin/env bash
set -euo pipefail

# This script prompts for confirmation, so it must run on a TTY.
if [[ ! -t 0 || ! -t 1 ]]; then
  echo "install-hook.sh must be run interactively (stdin/stdout must be a TTY)." >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="${HOME}/.config/claude-auto-permission"
CONFIG_FILE="${CONFIG_DIR}/config.txtpb"
STARTER_CONFIG="${SCRIPT_DIR}/starter-config.txtpb"
CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-${HOME}/.claude}"
SETTINGS_FILE="${CLAUDE_DIR}/settings.json"
BINARY_NAME="claude-auto-permission"

bold="\033[1m"
dim="\033[2m"
green="\033[32m"
yellow="\033[33m"
reset="\033[0m"

info()  { printf "  ${bold}%s${reset}\n" "$*"; }
note()  { printf "  ${dim}%s${reset}\n" "$*"; }
ok()    { printf "  ${green}%s${reset}\n" "$*"; }
skip()  { printf "  ${yellow}%s${reset}\n" "$*"; }

confirm() {
  printf "  %s [Y/n] " "$1"
  read -r reply
  [[ -z "${reply}" || "${reply}" =~ ^[Yy] ]]
}

# --- Step 1: Install binary ---

info "Step 1: Install binary"
if command -v "${BINARY_NAME}" &>/dev/null; then
  note "$(command -v "${BINARY_NAME}") already on PATH"
  if confirm "Reinstall (go install)?"; then
    go install "./cmd/${BINARY_NAME}"
    ok "Installed."
  else
    skip "Skipped."
  fi
else
  note "Will run: go install ./cmd/${BINARY_NAME}"
  if confirm "Install?"; then
    go install "./cmd/${BINARY_NAME}"
    ok "Installed to $(go env GOPATH)/bin/${BINARY_NAME}"
  else
    skip "Skipped."
  fi
fi
echo

# --- Step 2: Create starter config ---

info "Step 2: Create config"
if [[ -f "${CONFIG_FILE}" ]]; then
  skip "${CONFIG_FILE} already exists. Skipped."
else
  note "Will create ${CONFIG_FILE} with the following contents:"
  echo
  sed 's/^/    /' "${STARTER_CONFIG}"
  echo
  if confirm "Create config?"; then
    mkdir -p "${CONFIG_DIR}"
    cp "${STARTER_CONFIG}" "${CONFIG_FILE}"
    ok "Created ${CONFIG_FILE}"
  else
    skip "Skipped."
  fi
fi
echo

# --- Step 3: Register hook in Claude Code settings ---

info "Step 3: Register PreToolUse hook in Claude Code settings"
note "Settings file: ${SETTINGS_FILE}"

# Resolve the binary path for the hook command.
if command -v "${BINARY_NAME}" &>/dev/null; then
  hook_cmd="${BINARY_NAME}"
else
  hook_cmd="$(go env GOPATH)/bin/${BINARY_NAME}"
fi

# Check if jq is available (needed for JSON editing).
if ! command -v jq &>/dev/null; then
  skip "jq not found. Please manually add the hook to ${SETTINGS_FILE}:"
  note ""
  note '  "hooks": {'
  note '    "PreToolUse": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "'"${hook_cmd}"'" }] }]'
  note '  }'
  echo
  exit 0
fi

# A hook is "already registered" if any entry under .hooks.<event>[].hooks[]
# has a command containing the binary name. Substring match (rather than an
# exact-string match) tolerates `AWS_PROFILE=… claude-auto-permission --flag`
# and other env-var/flag prefixes the user might have added.
hook_already_installed_for() {
  local event="$1"
  [[ -f "${SETTINGS_FILE}" ]] && \
    jq -e --arg event "${event}" --arg name "${BINARY_NAME}" \
      '.hooks[$event][]? | .hooks[]? | select(.command? // "" | contains($name))' \
      "${SETTINGS_FILE}" &>/dev/null
}

# Register the hook for one event with the given matcher. Creates the
# .hooks.<event> array if it doesn't exist.
register_hook_for() {
  local event="$1"
  local matcher="$2"
  local entry
  entry='{"matcher":"'"${matcher}"'","hooks":[{"type":"command","command":"'"${hook_cmd}"'"}]}'

  jq --arg event "${event}" --argjson entry "${entry}" '
    .hooks //= {} |
    .hooks[$event] //= [] |
    .hooks[$event] += [$entry]
  ' "${SETTINGS_FILE}" > "${SETTINGS_FILE}.tmp" && mv "${SETTINGS_FILE}.tmp" "${SETTINGS_FILE}"
}

# Ensure settings file exists before any registration.
if [[ ! -f "${SETTINGS_FILE}" ]]; then
  mkdir -p "$(dirname "${SETTINGS_FILE}")"
  echo '{}' > "${SETTINGS_FILE}"
fi

# Single PreToolUse hook drives every tool call.
if hook_already_installed_for PreToolUse; then
  skip "PreToolUse hook already registered. Skipped."
else
  note "Will add PreToolUse hook (matcher: *) to ${SETTINGS_FILE}"
  note "  command: ${hook_cmd}"
  if confirm "Register PreToolUse hook?"; then
    register_hook_for PreToolUse "*"
    ok "Registered PreToolUse hook."
  else
    skip "Skipped."
  fi
fi
echo

ok "Done!"
