# End-to-end environment

Simulated WordPress hosts in Docker, reached over real SSH, for running `wp-ssh-bridge` against actual WP-CLI, MariaDB, rsync, and tar. The unit tests fake these binaries. This environment runs them, and its scenarios assert on the resulting databases and files.

The CLI only needs SSH, so there is no SFTP server. Each host runs `sshd` and gives the CLI what a shell account on a shared host would.

## Requirements

- Docker with Compose v2
- Go, to build the CLI under test
- Optional: DDEV, or Node.js/npm, plus `jq`, for the local runtime projects

## Start

```bash
e2e/wpsb-e2e up
e2e/wpsb-e2e test
```

`up` does four things:

1. It builds the CLI from the working tree, once for this machine and once for Linux.
2. It generates a throwaway login key and host key in `e2e/.state/`.
3. It starts the hosts.
4. It seeds them.

After changing Go code, run `e2e/wpsb-e2e build`. The containers pick up the new binary without a restart.

## The machines

| Machine | SSH alias | What it simulates |
| --- | --- | --- |
| `source` | `wpsb-source` (127.0.0.1:22221) | The production site. Prefix `abc_`. Its database also holds a second installation (`abc_shop_`) and another application (`other_app_sessions`). It runs a sample of the blocked plugins, one per category (`host/fixtures/blocked-plugins.txt`), next to an ordinary one, all active, plus an mu-plugin that stands in for Elementor and Bricks. |
| `legacy` | `wpsb-legacy` (127.0.0.1:22222) | The same site and database behind a hardened shared host: no rsync, a `php.ini` that disables `exec()` (Hostinger), and a MariaDB client without its `mariadb`/`mariadb-dump` names (Netcup). |
| `target` | `wpsb-target` (127.0.0.1:22223) | The push target, and the new server a clone runs on. The login key is installed there as a client key too, so it can reach the source. |
| `workstation` | — | A developer machine in standalone mode. `/work/.wp-ssh.yaml` points at the source and the target, and `/work/public` holds an older local copy under the `wp_` prefix. |

All four run on one image (`host/Dockerfile`). A shared MariaDB container holds one database per machine. Change the host ports with `WPSB_E2E_SOURCE_PORT`, `WPSB_E2E_LEGACY_PORT`, and `WPSB_E2E_TARGET_PORT`.

## Use the aliases from this machine

The CLI calls plain `ssh`, and rsync does too. `e2e/.state/bin/ssh` is a wrapper that points both at an isolated config, so the aliases work without touching `~/.ssh/config`, `known_hosts`, or your SSH agent:

```bash
eval "$(e2e/wpsb-e2e env)"   # puts the wrapper and the host CLI first on PATH
ssh wpsb-source 'wp --path=/var/www/site option get home'
```

Or for a single command: `e2e/wpsb-e2e run wp-ssh-bridge pull --silent`.

## Scenarios

Every pull scenario checks the blocked-plugin cleanup:

- None of the sampled plugins arrives or stays active, and the ordinary plugin does.
- Each runtime also gets one blocked plugin placed locally beforehand, so the removal of a local copy runs everywhere: UpdraftPlus in standalone, WP Mail SMTP in DDEV, Cloudflare in wp-env.
- A clone keeps them all.

Scenarios come in three suites:

- `containers`: the default; needs only Docker
- `ddev`: also starts a DDEV project on this machine
- `wp-env`: also starts a wp-env project on this machine

`e2e/wpsb-e2e test ddev` runs a suite, `e2e/wpsb-e2e test all` runs all three, and `e2e/wpsb-e2e test pull clone-db-prefix` runs single scenarios. `e2e/wpsb-e2e test --list` lists them all. Each scenario reseeds the machines it uses. The output of the CLI goes to `e2e/.state/last-run.log`.

In CI, `.github/workflows/ci.yml` runs each suite as its own job, in parallel. The suites start only after the unit tests and lint have passed, and only when Go code, `go.mod`, `e2e/`, or the workflow itself changed. A runner has no global DDEV configuration or SSH agent that could get in the way. When a job fails, the log and the container output are uploaded as an artifact.

Seed a machine by hand with `e2e/wpsb-e2e seed <machine>`. The machines are:

- `source`
- `target`: empty directory and database
- `target-config`: core and `wp-config.php` only, as the migration guide prepares a new host
- `target-site`: an installed site, with the prefix from `SEED_PREFIX`
- `workstation`

Look around with:

- `e2e/wpsb-e2e exec <machine>`: a shell as the SSH user
- `e2e/wpsb-e2e wp <machine> …`: WP-CLI on that machine's site

## Local DDEV and wp-env projects

These run on this machine and pull from `wpsb-source` through the wrapper, the same way a developer's project reaches a real host. The projects live in `e2e/.state/projects/`.

```bash
e2e/wpsb-e2e wp-env pull   # creates the project on port 8920 (WPSB_E2E_WPENV_PORT) on first use
e2e/wpsb-e2e ddev pull     # direct pull; `ddev native` runs `ddev pull wp-ssh` instead
```

After each pull the command prints the imported table prefix, `home` as stored in the database, and how many rows still carry the source URL. The `ddev` and `wp-env` suites reset the projects first and assert on the same values.

`e2e/wpsb-e2e ddev rm` and `e2e/wpsb-e2e wp-env rm` remove the projects.

## Stop

```bash
e2e/wpsb-e2e down             # keeps sites and databases
e2e/wpsb-e2e down --volumes   # drops them too
```

## Limits

- Each host is a real Debian userland with its tools removed or restricted. It is not a copy of any hosting provider.
- The page builder commands are stand-ins that record that they ran. They do not rebuild any CSS.
- The keys in `e2e/.state/keys` are for these containers only. The containers bind their SSH ports to 127.0.0.1.
