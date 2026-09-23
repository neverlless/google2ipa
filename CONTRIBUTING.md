# Contributing

Thanks for helping! Bug reports, docs fixes and features are all welcome.
For anything bigger than a small fix, please open an issue first so we can agree on the approach.

## Development

Requirements: Go (latest stable), Docker (only for the integration test),
[golangci-lint](https://golangci-lint.run/) v2.

```sh
go test -race ./...
golangci-lint run ./...
go run ./cmd/google2ipa --config config.yaml --dry-run
```

Layout:

| Path | What lives there |
| --- | --- |
| `cmd/google2ipa` | Flags, logging, run loop |
| `internal/config` | Config loading and validation |
| `internal/reconcile` | The planner (pure, heavily tested) and the apply loop |
| `internal/google` | Google Directory API source |
| `internal/ipa` | FreeIPA JSON-RPC client |
| `internal/notify` | SMTP mails and templates |

New sync behavior belongs in `internal/reconcile/plan.go` with a table test in `plan_test.go`.

## Integration test

The integration tests run against a real FreeIPA server in a local container.
They are not part of CI. `hack/freeipa-up.sh` creates the least-privilege
service account described in [docs/freeipa-setup.md](docs/freeipa-setup.md).
The server is kept in the `g2i-ipa-data` volume: the first run installs it
(5–15 minutes), later runs only start it.

```sh
eval "$(./hack/freeipa-up.sh | tail -1)"                                          # Linux (edits /etc/hosts)
eval "$(IPA_HOST=g2i-ipa.orb.local SKIP_HOSTS=1 ./hack/freeipa-up.sh | tail -1)"  # OrbStack on macOS
go test -tags integration -run Integration -v ./internal/ipa/
docker stop g2i-ipa        # keeps the installed server; RESET=1 reinstalls from scratch
```

Run them when you change `internal/ipa` or the sync logic.

## Pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:` ...).
- Add or update tests; keep `go test -race ./...` and `golangci-lint` green.
- Update `README.md`, `config.example.yaml` and `CHANGELOG.md` when behavior or options change.
