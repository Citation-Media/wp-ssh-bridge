# Agent Instructions

This repository maintains `wp-ssh-bridge`, a Go CLI for pulling and pushing WordPress databases and files over SSH, with DDEV provider integration.

It is a monorepo with two parts:

| Part | Location | What it is |
| --- | --- | --- |
| Application | `cmd/`, `internal/`, `go.mod` at the repository root | The Go CLI. It stays at the root because that is what its module path and `go install` resolve to. |
| Documentation | `packages/documentation` | The Blume site that renders the MDX pages. An npm workspace. |
| npm wrapper | `packages/npm` | The `@citation-media/wp-ssh-bridge` npm package. Ships no binary; downloads the release matching its own version. Its `bin` stays `wp-ssh-bridge`, so commands are unaffected by the scope. |
| Documentation content | `docs/` | The MDX pages themselves, deliberately outside the site package. |

Writing documentation means editing `docs/`. Only changes to the site itself, its theme, the landing page, or `install.sh` touch `packages/documentation`.

## Keep The Agent Skill Current

Other agents may only see the CLI and the project skill, not `README.md`. When changing user-facing behavior, commands, troubleshooting guidance, configuration keys, environment variables, or DDEV workflows, update the skill in `skills/wp-ssh-bridge-cli/` in the same change.

Keep the skill concise. It should contain only knowledge agents need to correctly operate or troubleshoot the CLI:

- Current commands and flags agents should recommend.
- Important configuration keys and environment variables.
- DDEV-specific workflow rules and common failure fixes.
- Safety guidance for destructive operations such as push.
- Small verification steps users can run after pull or push.

Do not copy broad README narrative, release marketing, internal implementation details, or obsolete migration history into the skill.

The skill lives in `skills/wp-ssh-bridge-cli/`, the agentskills.io layout, and reaches users three ways at once:

- **As an APM package.** `apm.yml` at the repository root makes the repo installable with `apm install Citation-Media/wp-ssh-bridge`. Its `marketplace:` block is the source for the catalogue served at `/marketplace.json`; regenerate that file with `npm run skill:pack` after a version bump, which runs `apm pack` and copies the result into `packages/documentation/public/`.
- **From the documentation site.** `ai.skills` in `packages/documentation/blume.config.ts` points at `skills/`, so every build bundles the directory into `/.well-known/agent-skills/` with a discovery index. `npx skills add https://wp-ssh-bridge.citation.media` reads that index, so the skill installs from the site without touching Git.
- **As a plain archive**, unpacked by hand from the same URL.

Editing the skill therefore ships it on the next docs deploy. A `SKILL.md` with an invalid `name` or `description` is skipped with a build warning instead of published. Keep the layout as `skills/<name>/SKILL.md`: both APM and `npx skills` recognize it, and moving it would break all three routes.

When changing the embedded default blocked-plugin list, update the list and its tests. Do not mirror specific default plugin slugs into the skill unless agents need to mention them to operate or troubleshoot the CLI.

## Keep The Documentation Site Current

`docs/` holds the public documentation, published at https://wp-ssh-bridge.citation.media. When changing user-facing behavior, update the page that covers it in the same change:

- `docs/commands/{pull,push,clone}.mdx` for command behavior and flags, including the per-runtime tabs.
- `docs/configuration.mdx` for config keys, environment variables, and flags.
- `docs/how-it-works.mdx` for the pipeline, runtime detection, and safety rules.
- `docs/troubleshooting/` for new failure modes and their fixes.

Pages are MDX and may use Blume components such as `Tabs`, `Steps`, `CardGroup`, and `:::note` callouts. Write for a reader who knows WordPress but not this tool, and prefer a short explanation over a bare list of flags.

Verify documentation changes from the repository root. The changelog source reads GitHub Releases, so a token has to be present or the changelog builds empty:

```bash
export GITHUB_TOKEN="$(gh auth token)"
npm install
npm run docs:build
npm run docs:validate
```

Use `--isolated` when a dev server is running, so the build does not corrupt its runtime:

```bash
cd packages/documentation && npx blume build --isolated
```

The changelog is generated from GitHub Releases, so do not add changelog pages by hand. `CHANGELOG.md` remains the in-repository record and the source for release notes.



## Release And Deploy Pipeline

Tagging a release runs `.github/workflows/release.yml`:

1. Tests run, then binaries are built for macOS and Linux on `amd64` and `arm64` and stamped with the tag.
2. The archives and `checksums.txt` are attached to a GitHub release with generated notes.
3. The `npm` job sets `packages/npm`'s version to the tag and publishes it. It runs after the release exists, because the package downloads its binary from that release.

Each archive is attached twice, under its versioned name and under a version-free one, so `releases/latest/download/<name>` is a permanent link to the newest build. `checksums.txt` lists both names for the same digest, which lets `shasum -c --ignore-missing` verify whichever file was fetched.

`packages/documentation/public/install.sh` relies on that: the default install is a plain download from the permanent link with no version to resolve, and `--version` addresses the tag directly. Renaming the release artifacts means changing that script and the download table in `docs/installation.mdx` too.

The changelog is generated from GitHub Releases by the `github-releases` content source, authenticated with `GITHUB_TOKEN`. Release notes are what users read, so write them well.

The site is deployed to Cloudflare Workers as `wp-ssh-bridge-docs`, at https://wp-ssh-bridge.citation.media. Server output is required because the site hosts an MCP server at `/mcp`; every other route is prerendered.

### The First npm Publish

The CI job cannot create the package: a granular token can only be scoped to a package that already exists, or to the whole scope. The first version therefore goes out from a workstation, and CI takes over from the second.

Do it only after the repository is public and the matching GitHub release exists. `prepublishOnly` enforces that: `packages/npm/scripts/check-release.js` sends a HEAD request for all four archives and `checksums.txt` at the tag `v<version>` and refuses to publish when any is unreachable. Publishing a version whose release cannot serve the binary would break every install of it.

```bash
cd packages/npm
npm publish --dry-run          # shows the file list and runs the release check
npm publish --access public
```

Afterwards create a granular token limited to `@citation-media/wp-ssh-bridge` with read and write access, store it as `NPM_TOKEN` in the repository's `npm` environment, and every later release publishes itself.

The check can be bypassed with `WP_SSH_BRIDGE_SKIP_RELEASE_CHECK=1`, which is only correct when you are publishing a version whose release you are about to create.

### Infrastructure The Pipeline Expects

Cloudflare hosts only the documentation site: the `wp-ssh-bridge-docs` Worker with `wp-ssh-bridge.citation.media` attached in the dashboard, not in the generated Wrangler config, so it survives redeploys. Binaries need no infrastructure of their own; GitHub serves them.

Deployment runs through Cloudflare Workers Builds, connected to this repository. Its build settings must be:

| Setting | Value |
| --- | --- |
| Root directory | `packages/documentation` |
| Build command | `npm run build` |
| Deploy command | `npm run deploy` |
| Environment variable | `GITHUB_TOKEN`, needed for the changelog while the repository is private |

The deploy command matters: plain `npx wrangler deploy` cannot find the adapter's config at `dist/server/wrangler.json` and fails with "Could not detect a directory containing static files". The `deploy` script passes that config and pins the Worker name.

Nothing in GitHub Actions deploys the site. Workers Builds owns it end to end.

One consequence is worth knowing: the changelog is built from GitHub Releases, and publishing a release creates no commit, so the site keeps the previous changelog until the next push. Pushing the version bump for the release after the tag, rather than before, is enough to close that gap; a manual rebuild in the Cloudflare dashboard also works.

GitHub repository settings:

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `NPM_TOKEN` | Publish rights for `@citation-media/wp-ssh-bridge`, in the `npm` environment |

Cloudflare needs no credentials in GitHub: Workers Builds authenticates on its own side. `DOCS_SITE_URL` is likewise unused now that the origin is pinned in `blume.config.ts`; set `SITE_URL` as a Workers Builds environment variable if a preview deployment ever needs a different origin.

The npm package is scoped to `@citation-media`, so the organization owns it from the first publish and membership controls who can release it. The scope requires `--access public`, which the job passes and `publishConfig` also records. The job requests `id-token: write` and publishes with provenance, which npm only accepts from a public repository; while this one is private it publishes without provenance instead of failing.

The npm job uses an `npm` environment, so publishing can require a review.

## The Dependency Pins In The Root Manifest

The workspace root declares `astro` and `js-yaml` as dependencies and overrides `astro`. None of that is arbitrary; removing it reintroduces two concrete failures.

`@scalar/astro`, which blume pulls in for its API-reference feature, still peers on `astro <= 6`. Without the override npm hoists a vulnerable astro 6 into the root, the Cloudflare adapter binds to it and npm marks it invalid, while blume quietly builds against its own nested astro 7. The override collapses that to one astro and removes a critical advisory.

Pinning `astro` at the root then moves its js-yaml 4 into the hoisted position, and blume needs js-yaml 5 there. The build fails with `The requested module 'js-yaml' does not provide an export named 'binaryTag'`. Declaring `js-yaml` at the root restores it; the packages that genuinely need js-yaml 3 or 4 keep nested copies.

After changing any of this, run a real build. Neither failure shows up in `npm install`.

## Development

- Match the existing Go style and keep changes focused.
- Run `gofmt` on modified Go files.
- Run `go test ./...` before finishing code changes. If the sandbox blocks the default Go cache, use a writable cache such as `GOCACHE=/private/tmp/wp-ssh-bridge-go-cache go test ./...`.
- Do not commit generated local project files or machine-specific paths.
