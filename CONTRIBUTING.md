# Contributing

Thanks for your interest in improving spiffile-operator. Issues and pull
requests are welcome.

## Development setup

```bash
git clone https://github.com/PeterSR/spiffile-operator
cd spiffile-operator
```

Then the usual cycle:

```bash
make test    # go test ./...
make lint    # gofmt + go vet (golangci-lint if installed)
make build   # compile the operator binary
```

Or directly with `go test ./...` / `go build ./...`.

The Helm chart is linted with:

```bash
helm lint charts/spiffile-operator
helm template test charts/spiffile-operator --set webhook.enabled=true >/dev/null
```

`deploy/crd.yaml` and `charts/spiffile-operator/crds/crds.yaml` must stay
byte-identical — CI enforces it. Edit one, copy to the other.

## Pull requests

- Keep changes focused; unrelated refactors belong in their own PR.
- Add or update tests for behavior changes — the controller and webhook
  packages both have table-style tests to extend.
- Run `gofmt`, `go vet` and the tests before pushing; CI runs the same
  checks plus golangci-lint and govulncheck.
- Describe *why* in the PR body, not just what.

For substantial changes (new CRD fields, new source modes, behavior of the
bundle contract), please open an issue first to discuss the design —
[docs/store-backend.md](docs/store-backend.md) describes the invariants new
features must preserve.

## License

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), per its section 5 — no CLA, inbound = outbound.
