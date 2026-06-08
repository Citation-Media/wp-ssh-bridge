---
title: DDEV WP SSH Pull Add-On Notes
description: Maintainer notes for the wp-ssh DDEV add-on repository.
---

## Release Checklist

1. Run the local syntax checks from `README.md`.
2. Run `bats tests/test.bats` when Bats and DDEV are available.
3. Test install from a local checkout with `ddev add-on get /path/to/ddev-wp-ssh`.
4. Commit changes and create a semantic version release.
5. For public discovery, add the `ddev-get` GitHub topic. Keep the repository private for internal-only use.
