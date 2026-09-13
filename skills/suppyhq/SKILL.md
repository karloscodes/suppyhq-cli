---
name: suppyhq
description: Drive a SuppyHQ inbox from the command line. Read conversations and customers, save replies as drafts, and send them when the agent has permission. The CLI talks to the SuppyHQ Agents API over OAuth. Output is JSON.
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

Drive a SuppyHQ inbox from the command line. Read conversations, look up customers, write replies (or save as drafts). All commands return JSON.

## Honesty

You are an AI. The customer will know — every reply you send carries an attribution footer the SuppyHQ server appends automatically:

> Replied by **{your agent name}** on behalf of **{operator name}**.

Because of that footer, write naturally without faking the operator's identity. Do not sign the body with the operator's name. Do not write "From: Carlos" or "— Carlos" at the end. Do not say "I'm Carlos's assistant" — say what you'd say. The footer handles attribution; the body answers the question.

Match the operator's tone (formal vs casual, terse vs detailed) by reading their past replies in the same thread. Tone, not identity.

## Setup (one-time)

```bash
suppyhq auth login
suppyhq setup claude          # Claude Code: plugin + skill + MCP hint
# or: suppyhq setup agents    # every detected agent
```

Browser OAuth is the default — no credentials in your clipboard. Verify with:

```bash
suppyhq auth status
suppyhq doctor
```

## Agent contract

The CLI speaks the [37signals agent contract](https://github.com/basecamp/cli/blob/main/RUBRIC.md) in miniature:

- **Piped or `--json`:** success envelope `{ok, data, summary, breadcrumbs}`; errors `{ok:false, code, retryable, hint}` with typed exit codes.
- **`--agent`:** raw JSON `data` on success — use this from scripts and MCP-backed flows.
- **GET retries:** the CLI retries 429/5xx on reads (1s / 2s / 4s). **Writes never auto-retry** — a failed `reply` may still have queued.
- **Discovery:** `suppyhq commands --json` and `suppyhq help --agent`.

### MCP (optional)

Register native MCP tools backed by your signed-in account:

```bash
claude mcp add suppyhq -- suppyhq mcp
claude mcp add suppyhq-readonly -- suppyhq mcp --read-only
```

Three domain tools: `suppyhq_conversations`, `suppyhq_customers`, `suppyhq_identity`. Each takes `{"action":"...", "params":{...}}`. Call `action: "describe"` for schemas.

## Commands

### Read

```bash
suppyhq inbox                  # list open conversations
suppyhq thread <id>            # one conversation + messages
suppyhq customers              # list customers
```

All return JSON. Pipe through `jq` for filtering, or feed straight into an LLM for summarization.

### Reply: decide draft or send

**Default behavior: save as a draft.** Sending should be the exception, not the rule.

```dot
digraph reply_decision {
    "Did the operator explicitly say 'send', 'handle', 'answer for me'?" [shape=diamond];
    "suppyhq reply <id> --draft" [shape=box];
    "suppyhq reply <id>" [shape=box];

    "Did the operator explicitly say 'send', 'handle', 'answer for me'?" -> "suppyhq reply <id>" [label="yes"];
    "Did the operator explicitly say 'send', 'handle', 'answer for me'?" -> "suppyhq reply <id> --draft" [label="no, or unclear"];
}
```

**Save as draft (default):**

```bash
echo "<p>Your reply text.</p>" | suppyhq reply <conversation_id> --draft
```

The draft lands in the operator's composer. They review, edit, hit Send. **You're done — don't follow up to send it yourself.** Tell the operator something like *"Drafted in your composer. Open the conversation to review and send."*

**Send (only when explicitly asked):**

```bash
echo "<p>Your reply text.</p>" | suppyhq reply <conversation_id> --yes
```

On an interactive terminal, `suppyhq reply <id>` (without `--draft`) prompts **Send this reply? [y/N]** before anything goes out. Agents and scripts must pass **`--yes`** only after the operator confirms — or use **`--draft`** (default).

Goes out after a 30-second cancel window. Use send *only* when the operator's intent is unambiguous: they said "send", "handle", "answer for me", "ship it", or set up full-auto mode.

**Sending needs its own permission.** Intent is not enough on its own: if this agent wasn't granted `send`, `reply --yes` fails with `403 Forbidden` and nothing reaches the customer. When that happens, don't retry and don't look for another way to send. Save the same body with `--draft` and tell the operator: *"I don't have permission to send, so I saved it as a draft in your composer. To let me send, re-run `suppyhq auth login --allow-send` and tick Send replies."*

### Draft rules

These are the rules every agent must follow. They're short. Memorize them.

1. **One draft per conversation.** Calling `--draft` when a draft already exists **overwrites it**. The previous body is gone.
2. **Check before overwriting.** Run `suppyhq thread <id>` first. If the response shows a draft, ask the operator: *"There's already a draft from you/another agent. Replace it?"* Don't blow away their work.
3. **Don't send what you drafted.** Drafting and sending are separate operator actions. Save the draft, then stop. The operator presses Send.
4. **No timing trick will turn a draft into a send.** There's no `suppyhq draft` followed by `suppyhq promote`. Just `--draft` (save) or no flag (send). Pick the right one up front.
5. **Empty draft = no draft.** Don't autosave on every keystroke; the operator's composer already does that. You save when you have a complete reply.

## Examples

```bash
# triage
suppyhq inbox | jq '.[] | select(.status=="open") | {id, subject, customer: .customer.email}'

# read context, then draft
suppyhq thread 42 | jq '.messages'
echo "<p>Yes — out by Friday.</p>" | suppyhq reply 42 --draft

# operator asked you to handle while they're in meetings
echo "<p>Refunded.</p>" | suppyhq reply 42 --yes
```

## What the agent should do

- **Read first, write second.** Summarize what's open before composing replies.
- **Match the operator's tone, not their identity.** Read past replies for cadence; don't sign as them.
- **Default to draft mode** when the operator hasn't said "send" or "handle".
- **One thread at a time.** Don't batch replies across multiple threads.

## What the agent should NOT do

- **Don't sign the body with the operator's name.** No "— Carlos" or "From, Sarah". The footer attributes the reply.
- **Don't pretend to be the human.** No "I'm Carlos" or "Speaking on Carlos's behalf, …". Just answer.
- **Don't send without operator intent.** If they said "look at this", that's a read. Wait for "draft" or "reply".
- **Don't `auth logout` without being asked.**
- **Don't loop over `suppyhq inbox` to auto-reply** — that's a different product (server-side autoresponder).

## VIP customers

Each customer in the API has a `vip_at` field. **If it's truthy (a timestamp), the operator has flagged this customer as VIP — they get priority.** If null, it's a regular customer.

VIP is set by the operator (a click on the customer page in the app). Agents do NOT toggle VIP via the CLI — that's a deliberate operator gesture. Treat the field as read-only context.

### Use it for triage

`vip_at` is exposed on every customer in the API, and is also embedded on the inline `customer` of each `inbox` conversation. So you don't need an extra round-trip to learn which threads belong to VIPs.

```bash
# List VIP threads first in a triage summary
suppyhq inbox | jq '[.[] | select(.customer.vip_at != null)]'
```

In a triage summary, call out VIP threads first ("3 VIP threads waiting: …"). In a draft reply, the body itself doesn't change — VIP is about *order of attention*, not different language.

### What VIP doesn't mean

- It's not a tier or paid plan. It's an Apple-Mail-style flag the operator sets on individual customers.
- It doesn't unlock different actions. A VIP reply still goes through the same draft / send path.
- It doesn't override scopes. A read-only agent still can't reply, even to a VIP.

## Configuration

| | |
|---|---|
| Config file | `~/.suppyhq/config.json` (0600) |
| `SUPPYHQ_API_URL` | Override API host (default `https://app.suppyhq.com`) |
| `SUPPYHQ_CLIENT_ID` | Override on-disk client id |
| `SUPPYHQ_CLIENT_SECRET` | Override on-disk client secret |

Env vars take precedence over the config file.

## Scopes

Reading, drafting and sending are three separate permissions. The operator picks them on the consent screen when they run `suppyhq auth login`, and they're fixed on the token after that.

| Permission | Scope tokens | What you can do |
|---|---|---|
| **Read only** | `read` | `inbox`, `thread`, `customers`. List and inspect, no writes. Good for triage, audit and digest agents. |
| **Read + draft** | `read draft` | Everything above, plus `reply --draft`. The draft lands in the operator's composer and reaches nobody until a human sends it. This is what `suppyhq auth login` asks for by default. |
| **Read + draft + send** | `read draft send` | Everything above, plus `reply --yes`. The customer's email carries a footer naming this agent. Only granted when the operator ticks Send replies, which `suppyhq auth login --allow-send` pre-ticks. |

Agents connected before drafting and sending were split may hold `read reply`. That older permission can still draft and send.

What each missing permission looks like:

- No `draft`: `reply --draft` returns `403 Forbidden`. Tell the operator you can only read.
- No `send`: `reply --yes` returns `403 Forbidden`. Save the body with `--draft` instead and tell the operator (see "Reply: decide draft or send").

Never try to get around a missing permission, for example by pasting the body into a note or asking the operator for another agent's credentials. The permission is the operator's decision.

## Rate limits

The CLI retries **read** requests on 429/5xx (1s, 2s, 4s). **Do not retry writes yourself** — if `reply` fails with 429, check the thread before trying again; the CLI marks write 429 as `retryable: false`.

If you are calling the API outside the CLI and hit 429, use this schedule:

1. Wait **1 second**, retry once.
2. Still 429? Wait **2 seconds**, retry once.
3. Still 429? Wait **4 seconds**, retry once.
4. Still 429 after the third retry (~7s of backoff total)? **The server is under load — hold for 60 seconds**, then make one final attempt.
5. Still 429 after the hold? Stop. Tell the operator the server is busy and ask them to try again in a few minutes.

Three quick retries, one long hold, then surface. The long hold matters: getting 429 three times in a row means the rate-limiter isn't a per-second cap — the server is genuinely under high load, and hammering it makes things worse. Sit out a minute. Don't loop forever.

Don't retry on any other status — `4xx` is permanent (fix the input), `5xx` likely means a real outage (don't pile on).

## When something goes wrong

- `not authenticated` → `suppyhq auth login`
- `401 Unauthorized` → token rejected; rerun `suppyhq auth login` and re-paste credentials
- `403 Forbidden` → the agent doesn't have the permission for that action. On a send, save the body with `--draft` instead. Permissions are fixed when the agent is authorized, so adding one means the operator re-runs `suppyhq auth login` (`--allow-send` for sending) and ticks the box.
- `429 Too Many Requests` → see "Rate limits" above. Retry with the 1s / 2s / 4s schedule, then give up.
