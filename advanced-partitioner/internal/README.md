# internal

Implementation packages. Only `cmd/*` may import these. External consumers
must use the binaries, not the libraries.

See `../README.md` for the package list and one-line purpose per package,
and `../BOOKKEEPING.md` section C for the migration phase order.

**Rule:** only `internal/executor` may import `os/exec`. Everything else
runs commands through the `Runner` interface. This is the project-specific
safety rule defined in the migration brief.
