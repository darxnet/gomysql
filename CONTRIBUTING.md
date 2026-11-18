# Contributing

`gomysql` is maintained by one person; issues and pull requests are welcome.
Please open an issue first for anything bigger than a bug fix, so the design is
agreed before the code is written.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org) are required: the
release workflow computes the next version from the commit subjects since the
last tag when a commit lands on `main`.

| Subject | Release |
|---|---|
| `fix:` | patch |
| `feat:` | minor |
| `feat!:` / `feat(x)!:` | minor while v0, not allowed on v1+ |
| `chore:`, `docs:`, `test:`, `ci:`, `refactor:`, `perf:` | none |

Commit bodies never affect the version. A `perf:` change that users should
know about belongs in a `feat:` commit.

## Before a pull request

    go mod tidy
    go vet ./...
    go test -race -count=1 ./...
    golangci-lint run ./...                # .golangci.yml, `default: all`

Tests that need a server run when `GOMYSQL_ADDR` is set (for example
`127.0.0.1:3306`); see the test files for the users they expect.

## Changes on a hot path

Include `benchstat` output in the pull request description:

    go test -run=NONE -bench=. -benchmem -count=6 ./... > old.txt   # on main
    go test -run=NONE -bench=. -benchmem -count=6 ./... > new.txt   # on your branch
    benchstat old.txt new.txt

A change that makes a benchmark slower needs a reason in the commit message.

## Ground rules

- Every behaviour change comes with a test.
- No new dependencies: the module uses only the standard library.
  Comparisons with other drivers live in the separate `bench` module.
