#!/usr/bin/env bash
# suppyhq-cli installer
#
# Usage:
#   curl -fsSL https://suppyhq.com/install-cli | bash
#   curl -fsSL https://suppyhq.com/install-cli | bash -s -- \
#     --client-id=ID --client-secret=SECRET [--api-url=https://app.suppyhq.com]
#
# Detects OS + arch, downloads the matching binary from the latest GitHub
# release of karloscodes/suppyhq-cli, verifies the SHA256 checksum, and
# installs into /usr/local/bin (or $HOME/.local/bin if non-root). When
# --client-id and --client-secret are passed (the post-create one-liner
# from app.suppyhq.com/agents), writes the config in one shot so the
# operator skips `suppyhq auth login`.
#
# Override the version with INSTALL_VERSION=v0.1.0.
set -euo pipefail

REPO="karloscodes/suppyhq-cli"
BIN="suppyhq"

# Parse flags. Quietly ignore unknown flags rather than aborting — the
# installer is curl-piped, so a typo from the operator should still install.
client_id=""
client_secret=""
api_url=""
for arg in "$@"; do
  case "$arg" in
    --client-id=*)     client_id="${arg#*=}" ;;
    --client-secret=*) client_secret="${arg#*=}" ;;
    --api-url=*)       api_url="${arg#*=}" ;;
  esac
done

err() { printf "\033[31merror:\033[0m %s\n" "$*" >&2; exit 1; }
info() { printf "\033[2m→\033[0m %s\n" "$*"; }
ok()   { printf "\033[32m✓\033[0m %s\n" "$*"; }

require() {
  command -v "$1" >/dev/null 2>&1 || err "'$1' not found. Please install it and retry."
}

require curl
require tar
require uname

# Detect OS / arch.
os=""
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) err "unsupported OS: $(uname -s). Supported: macOS, Linux." ;;
esac

arch=""
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) err "unsupported arch: $(uname -m). Supported: amd64, arm64." ;;
esac

# Resolve version.
version="${INSTALL_VERSION:-latest}"
if [ "$version" = "latest" ]; then
  info "Resolving latest release…"
  version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' \
    | head -1 \
    | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/') \
    || err "could not resolve latest release"
  [ -n "$version" ] || err "empty version from GitHub API"
fi
version_no_v="${version#v}"

archive="suppyhq_${version_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${version}/${archive}"
checksums_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"

# Stage in a tempdir.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "Downloading ${archive}"
curl -fsSL "$url" -o "$tmp/$archive" || err "download failed: $url"

info "Verifying checksum"
curl -fsSL "$checksums_url" -o "$tmp/checksums.txt" || err "checksums download failed"
expected=$(grep " ${archive}\$" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || err "no checksum entry for $archive"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
  err "neither sha256sum nor shasum available — cannot verify download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch (expected $expected, got $actual)"

# Extract.
info "Extracting"
tar -C "$tmp" -xzf "$tmp/$archive" || err "extract failed"
[ -x "$tmp/$BIN" ] || err "binary missing in archive"

# Pick install dir.
install_dir="/usr/local/bin"
sudo_cmd=""
if [ ! -w "$install_dir" ]; then
  if [ "$(id -u)" = "0" ]; then
    :
  elif command -v sudo >/dev/null 2>&1; then
    sudo_cmd="sudo"
  else
    install_dir="$HOME/.local/bin"
    mkdir -p "$install_dir"
    info "Installing to $install_dir (no sudo found; ensure it's on your PATH)"
  fi
fi

target="$install_dir/$BIN"
$sudo_cmd install -m 0755 "$tmp/$BIN" "$target" || err "install to $target failed"

ok "Installed $BIN $version → $target"

# Auto-install the Agent Skill for every AI agent we can detect on the
# system. Cursor is intentionally skipped — it's project-scoped, not
# user-scoped, so installing it from $HOME would put a stray file in
# wherever the operator happened to run the curl from. They can install
# it from their project root with `suppyhq install-skill --target=cursor`.
detected_any=0
detect_and_install() {
  local agent_dir="$1" target_name="$2" label="$3"
  if [ -d "$HOME/$agent_dir" ]; then
    if "$target" install-skill --target="$target_name" >/dev/null 2>&1; then
      ok "Installed $label skill"
      detected_any=1
    fi
  fi
}
detect_and_install ".claude"          "claude"   "Claude Code"
detect_and_install ".codex"           "codex"    "Codex CLI"
detect_and_install ".config/opencode" "opencode" "OpenCode"

# No agent detected → fall back to Claude as the default; the file's
# tiny and harmless if they end up not using one.
if [ "$detected_any" = "0" ]; then
  "$target" install-skill >/dev/null 2>&1 \
    && ok "Installed Claude Code skill (default)"
fi

# If credentials were passed, write the config now so the operator can
# skip `suppyhq auth login`. Mode 0600 mirrors what the CLI writes itself.
if [ -n "$client_id" ] && [ -n "$client_secret" ]; then
  config_dir="${HOME}/.suppyhq"
  config_file="${config_dir}/config.json"
  mkdir -p "$config_dir"
  chmod 700 "$config_dir"
  resolved_api_url="${api_url:-https://app.suppyhq.com}"
  cat > "$config_file" <<JSON
{
  "api_url": "${resolved_api_url}",
  "client_id": "${client_id}",
  "client_secret": "${client_secret}"
}
JSON
  chmod 600 "$config_file"
  ok "Wrote credentials to ${config_file}"
fi

# Next-steps message.
cat <<EOF

Next:

EOF

if [ -n "$client_id" ] && [ -n "$client_secret" ]; then
  cat <<EOF
  1. Verify
       suppyhq auth status

EOF
else
  cat <<EOF
  1. Authenticate
       suppyhq auth login

EOF
fi

cat <<EOF
  2. Use it from your AI:
       Restart your Claude Code (or Cursor / Codex / OpenCode) session,
       then ask in plain English: "What's in my SuppyHQ inbox?" or
       "Draft a reply to the latest customer."

  Using a different AI? Run one of these to install the skill:
       suppyhq install-skill --target=cursor       # Cursor
       suppyhq install-skill --target=codex        # Codex CLI
       suppyhq install-skill --target=opencode     # OpenCode

Docs: https://suppyhq.com/agents
EOF
