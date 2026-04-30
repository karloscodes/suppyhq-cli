---
name: suppyhq
description: Drive a SuppyHQ inbox from the command line. Read conversations and customers, post replies (or save them as drafts). The CLI talks to the SuppyHQ Agents API using OAuth2 client-credentials. Output is JSON.
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
```

Paste the Client ID and Client Secret from `https://app.suppyhq.com/agents`. Verify with:

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

### Reply (two modes)

**Auto mode** — the reply goes out (default):

```bash
echo "<p>Refunded — credit lands in 5–10 days.</p>" | suppyhq reply <conversation_id>
```

The message queues for 30 seconds before send, cancellable from the operator UI. Use this when the operator asked you to *handle* the reply, not draft it.

**Draft mode** — saved for the operator to review and send manually:

```bash
echo "<p>Refunded — credit lands in 5–10 days.</p>" | suppyhq reply <conversation_id> --draft
```

The draft is written to the operator's composer (one draft per conversation — see below). The operator sees it prefilled when they open the thread, with a banner saying "Drafted by {your agent name} · {time ago}".

**Default to auto when the operator says "send", "handle", "answer", "reply for me".**
**Default to draft when the operator says "draft", "write", "compose", "show me", "what would you say".**
**When in doubt, use draft.**

### How drafts work

Each conversation has at most **one draft** — the operator's composer is the draft. The implications:

- If you call `--draft` and there's already a draft (yours, the operator's, or another agent's), your call **replaces it**. Last writer wins. The operator's typed-but-unsaved words can disappear if you save right after they typed.
- If the operator typed something and walked away, autosave preserved their text in the draft. Calling `--draft` blindly wipes their words.
- **Read the conversation first.** `suppyhq thread <id>` shows whether a draft exists in the response. If yes, ask the operator before overwriting: *"There's an existing draft from you/another agent. Replace it?"*
- When the operator clicks Send in the composer, the draft (your or theirs) is what goes out. Same content, no copy/paste.
- When the operator clicks Discard, the draft is wiped. The composer empties.

## Examples

```bash
# triage
suppyhq inbox | jq '.[] | select(.status=="open") | {id, subject, customer: .customer.email}'

# read context, then draft
suppyhq thread 42 | jq '.messages'
echo "<p>Yes — out by Friday.</p>" | suppyhq reply 42 --draft

# operator asked you to handle while they're in meetings
echo "<p>Refunded.</p>" | suppyhq reply 42
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

## Configuration

| | |
|---|---|
| Config file | `~/.suppyhq/config.json` (0600) |
| `SUPPYHQ_API_URL` | Override API host (default `https://app.suppyhq.com`) |
| `SUPPYHQ_CLIENT_ID` | Override on-disk client id |
| `SUPPYHQ_CLIENT_SECRET` | Override on-disk client secret |

Env vars take precedence over the config file.

## Scopes

Each agent is granted one or both of these scopes when it's created at `https://app.suppyhq.com/agents`:

- **`read`** — *Read conversations + customer profiles.* List open conversations, read messages, look up customer profiles and purchase history. Required for `inbox`, `thread`, and `customers`.
- **`reply`** — *Reply on your behalf.* Save a draft, or send a reply that queues for 30 seconds (cancellable in the operator UI). Required for `reply` (with or without `--draft`). Sent emails carry an attribution footer naming the agent.

Most agents have both. A read-only audit / triage agent might have only `read`.

## When something goes wrong

- `not authenticated` → `suppyhq auth login`
- `401 Unauthorized` → token rejected; rerun `suppyhq auth login` and re-paste credentials
- `403 Forbidden` → the agent doesn't have the required scope. The action's scope is in the error body. Edit the agent at `https://app.suppyhq.com/agents` to grant it.
