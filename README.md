# suppyhq-cli

Official CLI for [SuppyHQ](https://suppyhq.com). Drive your inbox from the terminal — or let an AI agent (Claude Code, Cursor, Codex, OpenCode) do it for you.

```bash
curl -fsSL https://suppyhq.com/install-cli | bash
suppyhq auth login
suppyhq setup claude
```

## What it does

| Command | What it does |
|---|---|
| `suppyhq auth login` | Browser OAuth (default). `--manual` for paste flow. |
| `suppyhq auth status` | Show who's authenticated. |
| `suppyhq setup claude` | Claude Code plugin + skill + MCP registration hint. |
| `suppyhq setup agents` | Skill + every detected coding agent. |
| `suppyhq doctor` | Check CLI, auth, skill, and plugin health. |
| `suppyhq mcp` | MCP server on stdin/stdout (domain gateway tools). |
| `suppyhq inbox` | List conversations. |
| `suppyhq thread <id>` | Show one conversation with messages. |
| `suppyhq customers` | List customers. |
| `suppyhq reply <id>` | Post a reply. Interactive TTY prompts before send; `--yes` skips prompt; `--draft` saves for review. |

## Agent contract

Structured output for humans and machines:

```bash
suppyhq inbox --json       # {ok, data, summary, breadcrumbs}
suppyhq inbox --agent      # raw data only (for scripts)
suppyhq commands --json    # full command catalog
suppyhq help --agent       # structured help for any command
```

Errors carry `code`, `retryable`, and `hint` with typed exit codes (auth=3, forbidden=4, rate_limit=5, …). GET requests retry on 429/5xx; **writes never auto-retry**.

## MCP

Register with any MCP client as a stdio server:

```bash
claude mcp add suppyhq -- suppyhq mcp
suppyhq mcp --read-only
suppyhq mcp --domains=conversations,customers
```

Three domain tools: `suppyhq_conversations`, `suppyhq_customers`, `suppyhq_identity`. Each dispatches `{"action":"...", "params":{...}}`. Use `action: "describe"` for schemas.

## Install

### Quick install (Linux / macOS)

```bash
curl -fsSL https://suppyhq.com/install-cli | bash
# Force a specific agent during install:
SUPPYHQ_SETUP_AGENT=claude curl -fsSL https://suppyhq.com/install-cli | bash
```

The installer downloads the binary, runs `suppyhq setup agents` (skill + best-effort agent connection), and prints PATH hints. Managed skills (`.managed-by-suppyhq-cli`) refresh automatically on `suppyhq upgrade` and the first run of each new version.

### Manual

Grab the [latest release](https://github.com/karloscodes/suppyhq-cli/releases/latest).

## Skill + plugin

```bash
suppyhq setup claude                    # plugin + skill + MCP hint
suppyhq install-skill --target=cursor   # Cursor (project-scoped)
npx skills add karloscodes/suppyhq-cli -a claude-code
```

Restart your agent session after installing.

## Configuration

| Source | Use |
|---|---|
| `~/.suppyhq/config.json` (0600) | Default. Created by `auth login`. |
| `SUPPYHQ_API_URL`, `SUPPYHQ_CLIENT_ID`, `SUPPYHQ_CLIENT_SECRET` | Env overrides. |

## Examples

```bash
suppyhq inbox --json | jq '.data[] | select(.status=="open")'
suppyhq thread 42 --agent | jq '.messages[-1]'
echo "<p>Yes — out by Friday.</p>" | suppyhq reply 42 --draft
suppyhq doctor
```

## Development

```bash
go test ./...
go build -o suppyhq .
```

## License

MIT — see [LICENSE](LICENSE).
