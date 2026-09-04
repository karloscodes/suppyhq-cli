#!/usr/bin/env bash
# suppyhq-cli installer
#
# Usage:
#   curl -fsSL https://suppyhq.com/install-cli | bash
#   curl -fsSL https://suppyhq.com/install-cli | bash -s -- \
#     --client-id=ID --client-secret=SECRET [--api-url=https://app.suppyhq.com]
#
# Options (via environment):
#   SUPPYHQ_BIN_DIR       Where to install the binary (default: ~/bin if on PATH,
#                         else ~/.local/bin if on PATH, else ~/.local/bin)
#   SUPPYHQ_VERSION       Specific version to install (default: latest)
#   INSTALL_VERSION       Alias for SUPPYHQ_VERSION (backward compatible)
#   SUPPYHQ_SKIP_SETUP    Set to 1 to skip post-install agent setup
#   SUPPYHQ_SETUP_AGENT   Which agent(s) `setup agents` connects:
#                         claude | codex | cursor | opencode | all | none
#                         Unset = auto-detect (connect one agent; if several,
#                         install the skill only and print per-agent commands).
#
# Detects OS + arch, downloads from the latest GitHub release, verifies SHA256,
# installs the binary, runs `suppyhq setup agents`, and optionally writes OAuth
# credentials when --client-id and --client-secret are passed.
set -euo pipefail

REPO="karloscodes/suppyhq-cli"
BIN="suppyhq"
BIN_DIR="${SUPPYHQ_BIN_DIR:-}"
VERSION="${SUPPYHQ_VERSION:-${INSTALL_VERSION:-}}"

if [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; then
  green() { printf '\033[32m%s\033[0m' "$1"; }
  red()   { printf '\033[31m%s\033[0m' "$1"; }
  bold()  { printf '\033[1m%s\033[0m' "$1"; }
  dim()   { printf '\033[2m%s\033[0m' "$1"; }
else
  green() { printf '%s' "$1"; }
  red()   { printf '%s' "$1"; }
  bold()  { printf '\033[1m%s\033[0m' "$1"; }
  dim()   { printf '\033[2m%s\033[0m' "$1"; }
fi

info()  { echo "  $(green "✓") $1"; }
step()  { echo "  $(bold "→") $1"; }
error() { echo "  $(red "✗ ERROR:") $1" >&2; exit 1; }

path_contains_dir() {
  local dir="$1"
  [[ ":$PATH:" == *":$dir:"* ]]
}

default_bin_dir() {
  if path_contains_dir "$HOME/bin"; then
    echo "$HOME/bin"
    return 0
  fi
  if path_contains_dir "$HOME/.local/bin"; then
    echo "$HOME/.local/bin"
    return 0
  fi
  echo "$HOME/.local/bin"
}

find_sha256_cmd() {
  if command -v sha256sum &>/dev/null; then
    echo "sha256sum"
  elif command -v shasum &>/dev/null; then
    echo "shasum -a 256"
  else
    error "No SHA256 tool found (need sha256sum or shasum)"
  fi
}

get_latest_version() {
  local url version api_json
  if url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest"); then
    version="${url##*/}"
    version="${version#v}"
    if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      echo "$version"
      return 0
    fi
  fi
  if api_json=$(curl -fsSL -H 'Accept: application/vnd.github+json' -H 'User-Agent: suppyhq-cli-installer' "https://api.github.com/repos/${REPO}/releases/latest"); then
    if [[ $api_json =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"v?([^\"]+)\" ]]; then
      version="${BASH_REMATCH[1]}"
      if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        echo "$version"
        return 0
      fi
    fi
  fi
  error "Could not determine latest version"
}

setup_path() {
  if ! path_contains_dir "$BIN_DIR"; then
    step "Add $(bold "$BIN_DIR") to your PATH:"
    echo ""
    case "${SHELL:-}" in
      */zsh)
        echo "    echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.zshrc"
        echo "    source ~/.zshrc"
        ;;
      */bash)
        echo "    echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.bashrc"
        echo "    source ~/.bashrc"
        ;;
      *)
        echo "    export PATH=\"${BIN_DIR}:\$PATH\""
        ;;
    esac
    echo ""
  fi
}

post_install_setup() {
  local bin="$BIN_DIR/$BIN"
  if ! "$bin" setup --help 2>/dev/null | grep -qE '^[[:space:]]+agents[[:space:]]'; then
    "$bin" install-skill >/dev/null 2>&1 || true
    return 0
  fi
  "$bin" setup agents || true
}

# Parse flags. Quietly ignore unknown flags — the installer is curl-piped.
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

main() {
  command -v curl >/dev/null 2>&1 || error "curl is required"
  command -v tar >/dev/null 2>&1 || error "tar is required"
  command -v uname >/dev/null 2>&1 || error "uname is required"

  local os arch platform
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin) os="darwin" ;;
    linux)  os="linux" ;;
    *) error "unsupported OS: $(uname -s). Supported: macOS, Linux." ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) error "unsupported arch: $(uname -m). Supported: amd64, arm64." ;;
  esac
  platform="${os}_${arch}"

  if [[ -z "$BIN_DIR" ]]; then
    BIN_DIR=$(default_bin_dir)
  fi
  mkdir -p "$BIN_DIR"

  if [[ -n "$VERSION" ]]; then
    local version="$VERSION"
    version="${version#v}"
    if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      error "Invalid version '${VERSION}'. Expected semver (e.g. 0.2.3)."
    fi
  else
    step "Resolving latest release…"
    version=$(get_latest_version)
  fi

  local archive="suppyhq_${version}_${os}_${arch}.tar.gz"
  local url="https://github.com/${REPO}/releases/download/v${version}/${archive}"
  local checksums_url="https://github.com/${REPO}/releases/download/v${version}/checksums.txt"

  local tmp
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  step "Downloading ${archive}"
  curl -fsSL "$url" -o "$tmp/$archive" || error "download failed: $url"

  step "Verifying checksum"
  curl -fsSL "$checksums_url" -o "$tmp/checksums.txt" || error "checksums download failed"
  local expected actual
  expected=$(awk -v f="$archive" '$2 == f || $2 == ("*" f) {print $1; exit}' "$tmp/checksums.txt")
  actual=$(cd "$tmp" && $(find_sha256_cmd) "$archive" | awk '{print $1}')
  [[ -n "$expected" && "$expected" == "$actual" ]] || error "checksum mismatch for $archive"

  step "Extracting"
  tar -C "$tmp" -xzf "$tmp/$archive" || error "extract failed"
  [[ -x "$tmp/$BIN" ]] || error "binary missing in archive"

  install -m 0755 "$tmp/$BIN" "$BIN_DIR/$BIN" || error "install to $BIN_DIR/$BIN failed"
  info "Installed $BIN v${version} → $BIN_DIR/$BIN"

  setup_path

  if [[ -n "$client_id" && -n "$client_secret" ]]; then
    local config_dir="${HOME}/.suppyhq"
    local config_file="${config_dir}/config.json"
    mkdir -p "$config_dir"
    chmod 700 "$config_dir"
    local resolved_api_url="${api_url:-https://app.suppyhq.com}"
    cat > "$config_file" <<JSON
{
  "api_url": "${resolved_api_url}",
  "client_id": "${client_id}",
  "client_secret": "${client_secret}"
}
JSON
    chmod 600 "$config_file"
    info "Wrote credentials to ${config_file}"
  fi

  echo ""
  if [[ "${SUPPYHQ_SKIP_SETUP:-}" == "1" ]]; then
    step "Skipping agent setup (SUPPYHQ_SKIP_SETUP=1)"
  else
    step "Installing agent skill and connecting coding agents"
    post_install_setup
  fi

  echo ""
  echo "  Next steps:"
  if [[ -n "$client_id" && -n "$client_secret" ]]; then
    echo "    $(bold "suppyhq auth status")            Verify credentials"
  else
    echo "    $(bold "suppyhq auth login")             Authenticate"
  fi
  echo "    $(bold "suppyhq setup claude")             Claude Code plugin + skill + MCP hint"
  echo "    $(bold "suppyhq setup agents")             Every detected agent"
  echo "    $(bold "suppyhq doctor")                   Check CLI, auth, skill, plugin"
  echo ""
  echo "  Docs: https://suppyhq.com/agents"
  echo ""
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
