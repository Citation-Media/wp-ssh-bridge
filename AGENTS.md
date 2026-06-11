# Agent Instructions

This repository maintains `wp-ssh-bridge`, a Go CLI for pulling and pushing WordPress databases and files over SSH, with DDEV provider integration.

## Keep The Agent Skill Current

Other agents may only see the CLI and the project skill, not `README.md`. When changing user-facing behavior, commands, troubleshooting guidance, configuration keys, environment variables, or DDEV workflows, update the skill in `.agent/skills/wp-ssh-bridge-cli/` in the same change.

Keep the skill concise. It should contain only knowledge agents need to correctly operate or troubleshoot the CLI:

- Current commands and flags agents should recommend.
- Important configuration keys and environment variables.
- DDEV-specific workflow rules and common failure fixes.
- Safety guidance for destructive operations such as push.
- Small verification steps users can run after pull or push.

Do not copy broad README narrative, release marketing, internal implementation details, or obsolete migration history into the skill.

When changing the embedded default blocked-plugin list, update the list and its tests. Do not mirror specific default plugin slugs into the skill unless agents need to mention them to operate or troubleshoot the CLI.

## Development

- Match the existing Go style and keep changes focused.
- Run `gofmt` on modified Go files.
- Run `go test ./...` before finishing code changes. If the sandbox blocks the default Go cache, use a writable cache such as `GOCACHE=/private/tmp/wp-ssh-bridge-go-cache go test ./...`.
- Do not commit generated local project files or machine-specific paths.
