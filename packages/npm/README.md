# @citation-media/wp-ssh-bridge

Pull, push, and clone WordPress databases and files over plain SSH, for DDEV, wp-env, or any local checkout.

This package installs the `wp-ssh-bridge` binary for your platform. It is published under the `@citation-media` scope; the command it provides is `wp-ssh-bridge`. Add it to a project so everyone on the team runs the same version, pinned by your lockfile:

```bash
npm install --save-dev @citation-media/wp-ssh-bridge
```

```bash
npx wp-ssh-bridge init
npx wp-ssh-bridge pull --silent
```

The binary is downloaded from the GitHub release matching this package's version and verified against its published SHA-256 checksum. It is fetched during `postinstall`, or on first use when install scripts were skipped.

macOS and Linux on x64 and arm64 are supported. The tool drives `ssh`, `rsync`, and WP-CLI, which is why Windows is not.

| Variable | Effect |
| --- | --- |
| `WP_SSH_BRIDGE_BINARY` | Use this binary instead of downloading one. |
| `WP_SSH_BRIDGE_REPO` | Download releases from another GitHub repository. |
| `WP_SSH_BRIDGE_SKIP_DOWNLOAD` | Skip the `postinstall` download. |

Full documentation: https://wp-ssh-bridge.citation.media
