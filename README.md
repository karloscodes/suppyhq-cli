# suppyhq-cli

[![CI](https://github.com/karloscodes/suppyhq-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/karloscodes/suppyhq-cli/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/karloscodes/suppyhq-cli)](https://github.com/karloscodes/suppyhq-cli/releases/latest) [![License](https://img.shields.io/github/license/karloscodes/suppyhq-cli)](LICENSE)

The CLI for [SuppyHQ](https://suppyhq.com). Work your inbox from the terminal, or let a coding agent work it for you: Claude Code, Codex, Cursor, Copilot CLI, Gemini CLI, or opencode.

## Install

```bash
curl -fsSL https://suppyhq.com/install-cli | bash
suppyhq auth login
```

The script downloads the binary of the [latest release](https://github.com/karloscodes/suppyhq-cli/releases/latest) for your Mac or Linux machine, checks its SHA-256, and runs `suppyhq setup agents`, which connects the coding agent it finds. To choose the agent: `curl -fsSL https://suppyhq.com/install-cli | SUPPYHQ_SETUP_AGENT=claude bash` (`claude`, `codex`, `cursor`, `opencode`, `all`, or `none`). Then `suppyhq auth login` signs you in.

## Use

```bash
suppyhq inbox                  # list conversations
suppyhq thread 42              # show one conversation with its messages
suppyhq customers              # list customers
suppyhq reply 42               # post a reply; asks you to confirm before it sends
suppyhq reply 42 --draft       # save a reply for review instead
suppyhq auth status            # show who is signed in
suppyhq doctor                 # check the CLI, the login, the skill, and the plugin
```

`auth login` opens the browser by default. Use `--manual` for a paste flow. It asks for read and draft access. Add `--allow-send` to also ask for permission to send replies.

## Coding agents

`suppyhq setup` installs the skill, which tells an agent how to work an inbox, and registers the `suppyhq` MCP server, which gives the agent its tools.

| Agent | Command |
|---|---|
| Claude Code | `suppyhq setup claude` |
| Cursor | `suppyhq setup cursor`, run from the repo you want it in |
| Codex | `suppyhq setup codex` |
| opencode | `suppyhq install-skill --target=opencode` |
| Every agent on this machine | `suppyhq setup agents` |

Restart the agent session after setup. Run `suppyhq doctor` to check the result.

The repo follows the [Agent Plugins](https://agent-plugins.org) 1.0.0 layout. `plugin.json` and `mcp.json` sit at the root, next to `skills/`. Each client manifest, such as `.claude-plugin/plugin.json` or `gemini-extension.json`, points at the same skill and server.

## MCP

Register the server with any MCP client as a stdio server:

```bash
claude mcp add suppyhq -- suppyhq mcp
suppyhq mcp --read-only
suppyhq mcp --domains=conversations,customers
```

The server has three domain tools: `suppyhq_conversations`, `suppyhq_customers`, and `suppyhq_identity`. Each one takes `{"action": "...", "params": {...}}`. Call the action `describe` to get its schema.

## Agent contract

Every command prints structured output for scripts and agents:

```bash
suppyhq inbox --json       # {ok, data, summary, breadcrumbs}
suppyhq inbox --agent      # raw data only
suppyhq commands --json    # the full command catalog
suppyhq help --agent       # structured help for any command
```

Errors carry `code`, `retryable`, and `hint`. The exit codes are typed: auth is 3, forbidden is 4, and rate limit is 5. GET requests retry on 429 and 5xx. Writes never retry.

## Configuration

| Source | Use |
|---|---|
| `~/.suppyhq/config.json` (mode 0600) | The default. `auth login` creates it. |
| `SUPPYHQ_API_URL`, `SUPPYHQ_CLIENT_ID`, `SUPPYHQ_CLIENT_SECRET` | Environment overrides |

## Examples

```bash
suppyhq inbox --json | jq '.data[] | select(.folder=="needs_you")'
suppyhq thread 42 --agent | jq '.messages[-1]'
echo "<p>Yes, out by Friday.</p>" | suppyhq reply 42 --draft
```

## Development

```bash
go test ./...
go build -o suppyhq .
```

The tests check that the manifests agree on name and version, that one `SKILL.md` exists, and that every command the skill mentions exists in `commandCatalog()`. Deliberate exceptions go in `.skill-drift-allowlist`.

## Releases

A tag `v*` starts a release through GitHub Actions.

## License

MIT. See [LICENSE](LICENSE).
