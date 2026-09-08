# gotorrent

`gotorrent` is a small BitTorrent v1 downloader written with the Go standard
library. It supports single-file and multi-file metainfo, HTTP(S) and UDP
trackers, tracker tiers, compact IPv4/IPv6 peer lists, piece verification,
bounded concurrent peer downloads, resumable partial files, and safe atomic
finalization.

## Requirements

- Go 1.26.1 or newer
- Network access to the torrent's trackers and peers

## Usage

```sh
go run . [flags] path/to/file.torrent
```

Available flags:

- `-output`: download root; defaults to `./torrents`
- `-port`: port reported to trackers; defaults to `6881`
- `-workers`: maximum concurrent peer connections; defaults to `30`
- `-idle-timeout`: fail when no piece completes for this duration; defaults to
  `5m`

Partial data is stored below `<output>/in-progress`. On restart, every existing
piece is re-hashed before being reused. A successful download is synced and
moved to `<output>/completed`; an existing completed destination is never
overwritten.

Paths from metainfo are treated as untrusted. Absolute paths, traversal,
separators inside path components, platform-reserved names, invalid UTF-8, and
symlinked partial-file targets are rejected.

## Development

Run the same mandatory checks used by the commit hook:

```sh
gofmt -w .
go mod tidy -diff
go mod verify
go vet ./...
go test -count=1 ./...
```

When CGO and a C compiler are available, also run:

```sh
go test -race -count=1 ./...
```

### Pre-commit hook

The repository includes a version-controlled hook at `.githooks/pre-commit`.
Activate it once per clone:

```sh
git config core.hooksPath .githooks
```

On Unix-like systems, ensure it is executable after checkout:

```sh
chmod +x .githooks/pre-commit
```

The hook checks staged whitespace, Go formatting, module tidiness and integrity,
runs `go vet`, and executes the full uncached test suite. It also runs race
tests when CGO and a compiler are available. If `staticcheck` or `govulncheck`
is installed, the corresponding optional check runs automatically.

## Scope

This is intentionally a BitTorrent v1 tracker/peer client. Magnet links, DHT,
peer exchange, protocol encryption, inbound seeding, and BitTorrent v2 are not
implemented. Peer and tracker inputs are size-limited and all network phases
have deadlines so unavailable or hostile endpoints fail cleanly.
