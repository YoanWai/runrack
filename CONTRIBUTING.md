# Contributing

Thanks for helping. Bug reports with `runrack doctor` output, provider research and pull requests are all welcome.

## Build and test

```sh
go build ./cmd/runrack
go vet ./...
go test -race ./...
```

Go 1.27 or newer. The only dependencies are Bubble Tea and Lip Gloss.

Try the UI without runners:

```sh
go run ./cmd/runrack --demo
```

## Golden files

The board, job detail and help screens are golden tested at 80x24, 120x33, 160x45 and 240x75, each with three looks: no color with ASCII glyphs, 16 colors with console glyphs, truecolor with Unicode glyphs. The tests also check that no line is wider than the screen and that the console look only uses runes the console font can draw.

After an intended change to the UI or to the `--json` shape:

```sh
go test ./internal/ui ./internal/demo -update
git diff internal/ui/testdata internal/demo/testdata
```

Look at the diff before committing it. The JSON golden must never lose a field.

## Fixtures

`testdata/` holds real line shapes captured from runner machines. Before adding a fixture, anonymize it:

- organizations and repositories become `acme`, `globex`, `acme/widgets`, `globex/site`
- runner names become `zenbook-acme-1` and containers `runner-acme-1`
- people become `octocat`
- keep every secret as the runner logged it (`***`), and never add a real token, URL with a token, or internal host name

No private organization, repository or person may appear in code, fixtures, docs or screenshots. Screenshots come from `runrack --demo`.

## Safety rules

runrack is read-only. Two tests guard that:

- every command a target builds must pass `target.Check`, and a list of dangerous commands must fail it
- an AST test fails on any `exec.Command` outside `internal/target`

If you need a new command, add it to the allowlist in `internal/target/allowlist.go` together with tests for what it allows and what it rejects.

## Adding a provider

A provider finds one kind of runner and reads its jobs. See `internal/provider/provider.go`:

1. `Claim` is pure. Given the process list (with cgroup placement) and the unit lists per target, return the runners you recognize. Match on the process name (`comm`), never on a command line substring.
2. `Inspect` reads the runner's identity through a `target.Target`.
3. `WorkerLogs` and `ReadJob` turn local job records into `state.Result` values. Return an empty conclusion while a job runs.

Put research notes for runners that are not implemented yet in `docs/providers.md`.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `chore:`), small and focused. No AI attribution lines.
