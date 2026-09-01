# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with 0.x semantics: minor versions may break the API.

## [Unreleased]

## [0.5.0] - 2026-09-02

### Added

- `SQLITE_CONSTRAINT_ROWID` translates to `rio.ErrDuplicateKey`.

### Changed

- `Open` and `New` enable rio's prepared-statement cache by default; the driver otherwise re-prepares every statement. `rio.WithoutStmtCache()` opts out.
- `Open` defaults `_timezone=UTC`, so driver-bound times store with the same `+00:00` offset as rio's own, and `_txlock=immediate`, so transactions take the write lock at `BEGIN`.
- rio v0.16.0.

## [0.4.1] - 2026-08-31

### Changed

- rio v0.13.0.

## [0.4.0] - 2026-08-30

### Changed

- rio v0.11.0.

## [0.3.1] - 2026-08-20

### Changed

- Go 1.27 and dependency updates.

## [0.3.0] - 2026-08-09

### Changed

- rio v0.10.0.

## [0.2.3] - 2026-07-11

### Changed

- rio v0.9.0; release automation.

## [0.2.2] - 2026-07-10

### Changed

- rio v0.7.0.

## [0.2.1] - 2026-07-10

### Changed

- rio v0.6.0.

## [0.2.0] - 2026-07-10

### Changed

- rio v0.5.0.

### Fixed

- Default pragmas apply to the empty (temporary) DSN.

## [0.1.1] - 2026-07-09

### Changed

- Documented the `_time_format` default and the `:memory:` pooling behavior.

## [0.1.0] - 2026-07-09

### Added

- Initial release: `Open` and `New`, `foreign_keys` and `busy_timeout` defaults, duplicate-key and foreign-key error translation.

[Unreleased]: https://github.com/go-rio/sqlite/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/go-rio/sqlite/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/go-rio/sqlite/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/go-rio/sqlite/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/go-rio/sqlite/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/go-rio/sqlite/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/go-rio/sqlite/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/go-rio/sqlite/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/go-rio/sqlite/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/go-rio/sqlite/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/go-rio/sqlite/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/go-rio/sqlite/releases/tag/v0.1.0
