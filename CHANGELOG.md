# Changelog

All notable changes to `wp-ssh-bridge` are documented in this file.

## [0.5.0] - 2026-07-12

### Added

- Add `migrate --clean-target` to remove pre-existing target content before syncing. The rsync transport uses `--delete`; the scp/tar transport empties the resolved WordPress directory while preserving operational files and refusing unsafe filesystem-root or home-directory targets.

### Changed

- Make migrations additive by default on both rsync and scp/tar transports. Normal non-migration pulls continue to mirror the source with rsync `--delete`.
- Use DDEV's resolved `DDEV_TLD` when deriving `additional_hostnames`, allowing custom project TLDs as well as `ddev.site`.

### Fixed

- Prevent protocol-less multisite domain mappings from rewriting a newly generated local hostname a second time. A mapping such as `acme-group.de` to `acme-group.de.ddev.site` now updates bare domains and complete URLs exactly once ([#13]).

[0.5.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.4.1...v0.5.0
[#13]: https://github.com/Citation-Media/wp-ssh-bridge/issues/13
