---
name: suppyhq
description: Drive a SuppyHQ inbox from the command line. Read conversations and customers, post replies. The CLI talks to the SuppyHQ Agents API using OAuth2 client-credentials. Output is JSON, intended for AI agents and humans alike.
triggers:
  - suppyhq
  - /suppyhq
  - suppy
  - suppyhq inbox
  - suppyhq reply
  - suppyhq customers
  - suppyhq thread
invocable: true
argument-hint: "[command] [args...]"
---

# SuppyHQ

Drive a SuppyHQ inbox from the command line. Triage conversations, look up customers, draft and send replies — all returning JSON for chaining or piping to an LLM.

## Setup (one-time)

```bash
suppyhq auth login
```

Interactive — paste the Client ID and Client Secret from `https://app.suppyhq.com/agents`. Credentials land in `~/.suppyhq/config.json` (mode 0600).

Verify with:

```bash
suppyhq auth status
```

## Commands

### Read

```bash
suppyhq inbox                  # list open conversations
suppyhq thread <id>            # one conversation + messages
suppyhq customers              # list customers
```

All return JSON. Pipe through `jq` for filtering, or feed straight into an LLM for summarization.

### Reply

```bash
suppyhq reply <conversation_id> "<p>HTML body</p>"
```

Or pipe the body via stdin (preferred — no shell escaping):

```bash
echo "<p>Sent from the CLI.</p>" | suppyhq reply 42
```

Replies go through the **same delayed-send window** as a reply typed in the web UI: 30 seconds of "pending" with a Cancel button visible to the operator. Don't send a reply on behalf of the operator without confirming.

## Examples

**Triage what's waiting**

```bash
suppyhq inbox | jq '.[] | select(.status=="open") | {id, subject, customer: .customer.email}'
```

**Show the latest message in a thread**

```bash
suppyhq thread 42 | jq '.messages[-1]'
```

**Draft + confirm + send**

```bash
DRAFT=$(echo "<p>Hi — yes, that's the next release. Out by Friday.</p>")
echo "$DRAFT" | suppyhq reply 42
```

## What the agent should do

- **Confirm before replying.** Always show the operator the draft and the conversation id, get a yes, *then* run `suppyhq reply`.
- **Read first, write second.** When triaging, prefer summarizing what's open over composing replies unsolicited.
- **Match the operator's voice.** Quote past replies they've sent in this thread (`suppyhq thread <id>`) before drafting a new one.
- **One thread at a time.** SuppyHQ's whole stance is that operators handle one customer at a time. Don't queue up batched replies.

## What the agent should NOT do

- Don't send replies without explicit operator confirmation.
- Don't `auth logout` without being asked.
- Don't loop over `suppyhq inbox` to auto-reply — that's a different product (autoresponder, configured server-side).

## Configuration

| | |
|---|---|
| Config file | `~/.suppyhq/config.json` (0600) |
| `SUPPYHQ_API_URL` | Override API host (default `https://app.suppyhq.com`) |
| `SUPPYHQ_CLIENT_ID` | Override on-disk client id |
| `SUPPYHQ_CLIENT_SECRET` | Override on-disk client secret |

Env vars take precedence over the config file.

## When something goes wrong

- `not authenticated` → `suppyhq auth login`
- `401 Unauthorized` → token rejected; rerun `suppyhq auth login` and re-paste credentials
- `403 Forbidden` → the agent's access list (Read / Reply) doesn't cover this action. Edit the agent at `https://app.suppyhq.com/agents`.
