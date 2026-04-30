## What

<!-- One-liner: what does this PR change? -->

## Why

<!-- Linked issue or one paragraph on the motivation. -->

## Checklist

- [ ] `go test -race -v ./...` passes locally
- [ ] `go vet ./...` passes
- [ ] If this changes the OAuth/PKCE flow, the SuppyHQ server (oauth-agents) was updated in lockstep
- [ ] If this changes the SKILL.md, restarted Claude Code locally and verified the new instructions land
