# Contributing

## Development setup

```bash
go mod download
make tidy
```

## Branch naming

- `feat/<short-name>`
- `fix/<short-name>`
- `chore/<short-name>`
- `docs/<short-name>`
- `ci/<short-name>`

## Commits

Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `chore:`, `test:`, `ci:`.

## Tests

```bash
make all           # vet + test + build
make test
make race
make verify-example
make e2e           # builds binary then runs demo pipeline
```

## Pull requests

Open against `main`. CI must be green. Squash merge preferred.

Checklist:

- [ ] `make all` passes
- [ ] Branch uses `feat/` / `fix/` / `chore/` / `docs/` / `ci/`
- [ ] Conventional commits
- [ ] Contract / evidence changes include tests under `internal/`
- [ ] Examples still verify (`make verify-example`)
