# WP SSH Bridge

`wp-ssh-bridge` is a Go CLI that pulls and pushes WordPress databases and files over SSH, for hosts that offer nothing but SSH. It is a single binary on your machine and works with DDEV, wp-env, or a standalone WordPress checkout.

## Documentation

The documentation lives at **https://wp-ssh-bridge.citation.media**. This README only covers installing the CLI and working on this repository.

- [Quickstart](https://wp-ssh-bridge.citation.media/docs/quickstart) and [Installation](https://wp-ssh-bridge.citation.media/docs/installation)
- Commands: [pull](https://wp-ssh-bridge.citation.media/docs/commands/pull), [push](https://wp-ssh-bridge.citation.media/docs/commands/push), [clone](https://wp-ssh-bridge.citation.media/docs/commands/clone), and [Migrate a site](https://wp-ssh-bridge.citation.media/docs/migrate-a-site)
- [Configuration](https://wp-ssh-bridge.citation.media/docs/configuration): every config key, environment variable, and flag
- [How it works](https://wp-ssh-bridge.citation.media/docs/how-it-works): runtime detection, the pipelines, and the safety rules
- [SSH access](https://wp-ssh-bridge.citation.media/docs/ssh-access) and [Troubleshooting](https://wp-ssh-bridge.citation.media/docs/troubleshooting)

## Install

As a dev dependency of the project, which pins the version in `package.json`:

```bash
npm install --save-dev @citation-media/wp-ssh-bridge
npx wp-ssh-bridge init
npx wp-ssh-bridge pull --silent
```

Without Node, the install script picks the build for the machine, verifies its checksum, and installs into `.ddev/bin/` in a DDEV project or into the current directory otherwise:

```bash
curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
```

[Installation](https://wp-ssh-bridge.citation.media/docs/installation) covers the script options, updating, building from source, and the agent skill.

## Repository layout

This repository is a monorepo:

| Part | Location |
| --- | --- |
| Go CLI | `cmd/`, `internal/`, `go.mod` at the root |
| Documentation pages | [`docs/`](docs/) |
| Documentation site (Blume) | [`packages/documentation/`](packages/documentation/) |
| npm wrapper | [`packages/npm/`](packages/npm/) |
| Agent skill | [`skills/wp-ssh-bridge-cli/`](skills/wp-ssh-bridge-cli/) |

## Development

```bash
go test ./...
go build ./cmd/wp-ssh-bridge
```

Work on the documentation site:

```bash
npm install
npm run docs:dev
```

Releases are cut from version tags. The release and deploy pipeline, and the rules for keeping the docs and the agent skill current, are in [`AGENTS.md`](AGENTS.md).
