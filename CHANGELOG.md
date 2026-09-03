# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with 0.x semantics: minor versions may break the API.

## [Unreleased]

## [0.6.1] - 2026-09-02

### Changed

- rio v0.18.0.

## [0.6.0] - 2026-09-02

### Added

- `Open` serves rio's native channel: statements run directly against the embedded SQLite library (`modernc.org/sqlite/lib`), every connection keeps a bounded prepared-statement cache, and rows decode by storage class straight into rio's scan cells. Against database/sql with the statement cache, reading 100 rows is about 3× faster and a point read about 2.4×, with an order of magnitude fewer allocations.
- `OpenSQL` returns a plain `*sql.DB` over the same connection layer, the handle go-rio/migrate consumes; `db.Unwrap()` serves the same kind of view wherever a second connection can reach the database.
- `Pool` and `PoolOf`: the connection cap (`MaxConns`, `SetMaxConns`), `GOMAXPROCS` by default and one connection for private memory, temporary, and shared-cache databases.
- `Error` carries the extended result code (`Code`) and message of every SQLite failure.
- DSN shorthands `_busy_timeout`/`_timeout`, `_auto_vacuum`/`_vacuum`, `_foreign_keys`/`_fk`, `_journal_mode`/`_journal`, `_synchronous`/`_sync`, and `_query_only`, applied in the modernc driver's order; a shorthand also satisfies a default.
- A canceled context interrupts the running statement and surfaces the context's error.

### Changed

- Times decode at UTC from every SQLite text form (date, `T` or space separator, fraction, `Z` or offset), both into `time.Time` fields and from `DATE`, `DATETIME`, and `TIMESTAMP` columns.
- Read-only transactions begin deferred; every isolation level is accepted, SQLite being serializable. A failed `COMMIT` rolls the connection back before it returns to the pool.
- rio v0.17.0, whose `NativeLastInserter` backfills a lone auto-increment key from `sqlite3_last_insert_rowid`.

### Removed

- `New` and the database/sql channel over the modernc driver. `rio.WithStmtCache` now panics in `rio.NewNative`; the per-connection cache replaces it.
- The `_time_format` and `_timezone` parameters: values bind and decode at UTC. Unknown underscore parameters are rejected.

## [0.5.1] - 2026-09-02

### Added

- `CONTRIBUTING.md`, `CHANGELOG.md`, `llms.txt`, and compile-only examples for `Open` (file, shared-memory, WAL) and `New`.

### Changed

- README restructured; package-level defaults precede the constructors in the source. No API change.

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

[Unreleased]: https://github.com/go-rio/sqlite/compare/v0.6.1...HEAD
[0.6.1]: https://github.com/go-rio/sqlite/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/go-rio/sqlite/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/go-rio/sqlite/compare/v0.5.0...v0.5.1
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
