# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Snapshot results and errors when cancellation interrupts Map or MapDone launch loops.
- Honor a single MapDone done signal while a batch is full and avoid retaining cancellation waiters.

### Changed
- Update compatible dependencies and pin supported Go and lint tooling; Go 1.26.11 or later is required.

## Roadmap

- TBD

## [1.1.0] - 2022-07-08

## Added
- Added `RunCh` which call concurrently the given function with the given context returning a channel of results.

### Changed
- `Run` now requires `context`.

## [1.0.1] - 2022-07-08
### Added
- First release.
