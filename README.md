# concurrentloop

[![Go](https://github.com/thalesfsp/concurrentloop/actions/workflows/go.yml/badge.svg)](https://github.com/thalesfsp/concurrentloop/actions/workflows/go.yml)

`concurrentloop` provides a function to call a function concurrently powered by generics, channels, and goroutines.

## Install

Requires Go 1.26.11 or later. Development uses Go 1.27.4 and golangci-lint 2.15.0, pinned in `mise.toml`; run checks with `mise exec -- make lint test coverage`. CI reads the minimum Go version from `go.mod`.

`$ go get github.com/thalesfsp/concurrentloop`

### Specific version

Example: `$ go get github.com/thalesfsp/concurrentloop@v1.2.0`

## Usage

See [`example_test.go`](example_test.go), and [`concurrentloop_test.go`](concurrentloop_test.go) file.

### Documentation

Run `$ make doc` or check out [online](https://pkg.go.dev/github.com/thalesfsp/concurrentloop).

## Development

Check out [CONTRIBUTION](CONTRIBUTION.md).

### Release

1. Update [CHANGELOG](CHANGELOG.md) accordingly.
2. Once changes from MR are merged.
3. Tag and release.

## Roadmap

Check out [CHANGELOG](CHANGELOG.md).
