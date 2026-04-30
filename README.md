# suppyhq-cli

Official CLI for [SuppyHQ](https://suppyhq.com). Drive your inbox from the terminal — or let an AI agent (Claude Code, Cursor, Codex, OpenCode) do it for you.

```bash
curl -fsSL https://suppyhq.com/install-cli | bash
suppyhq auth login
suppyhq install-skill                # Claude Code
```

## What it does

| Command | What it does |
|---|---|
| `suppyhq auth login` | Interactive setup. Paste Client ID + Secret from `app.suppyhq.com/agents`. |
| `suppyhq auth status` | Show who's authenticated. |
| `suppyhq auth logout` | Forget credentials. |
| `suppyhq install-skill` | Install the Claude Code skill into `~/.claude/skills/suppyhq/`. |
| `suppyhq install-skill --target=cursor` | Same, for Cursor / Codex / OpenCode. |
| `suppyhq inbox` | List conversations (JSON). |
| `suppyhq thread <id>` | Show one conversation with messages (JSON). |
| `suppyhq customers` | List customers (JSON). |
| `suppyhq reply <id> "<html>"` | Post a reply. Body via 2nd arg or stdin. |

All read commands return JSON — pipe to `jq`, feed to an LLM, or just read it.

## Install

### Quick install (Linux / macOS)

```bash
curl -fsSL https://suppyhq.com/install-cli | bash
```

Detects OS + arch, downloads the matching binary from GitHub Releases, drops it in `/usr/local/bin/suppyhq` (or `~/.local/bin` if non-root).

### Manual

Grab the [latest release](https://github.com/karloscodes/suppyhq-cli/releases/latest), unpack the `tar.gz` for your platform, drop `suppyhq` in your `$PATH`.

Supported platforms: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`.

## Skill installation (for AI agents)

Two ways:

```bash
# Built-in (no Node required)
suppyhq install-skill                          # Claude Code (default)
suppyhq install-skill --target=cursor          # Cursor
suppyhq install-skill --target=codex           # Codex CLI
suppyhq install-skill --target=opencode        # OpenCode
suppyhq install-skill --target=all             # All of the above
```

```bash
# Or via the open Agent Skills standard
npx skills add karloscodes/suppyhq-cli -a claude-code
```

Restart your AI agent session after installing the skill so it picks it up.

## Configuration

| Source | Use |
|---|---|
| `~/.suppyhq/config.json` (0600) | Default. Created by `auth login`. |
| `SUPPYHQ_API_URL`, `SUPPYHQ_CLIENT_ID`, `SUPPYHQ_CLIENT_SECRET` | Env vars. Override the config file. |

## Authentication

OAuth2 client-credentials grant. The CLI exchanges your Client ID + Secret for a short-lived Bearer token on each invocation. No long-lived tokens, no refresh dance.

## Examples

```bash
# What's waiting on me?
suppyhq inbox | jq '.[] | select(.status=="open") | {id, subject, customer: .customer.email}'

# Latest message in a thread
suppyhq thread 42 | jq '.messages[-1]'

# Draft + send (with shell escape) or stdin (cleaner)
echo "<p>Yes — out by Friday.</p>" | suppyhq reply 42
```

## Development

```bash
go test ./...
go build -o suppyhq .
./suppyhq help
```

Releases are built by [GoReleaser](https://goreleaser.com) on tag push (`v*`). See `.goreleaser.yaml`.

## License

MIT — see [LICENSE](LICENSE).
