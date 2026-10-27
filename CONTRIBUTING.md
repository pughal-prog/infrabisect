# Contributing to InfraBisect

Thanks for helping! Bug reports, new sources and better docs are all welcome.

## Development

You need Go 1.23+, git, and a POSIX `sh` (on Windows, the one from Git for Windows) for the end-to-end tests.

```bash
make test      # unit + end-to-end tests (dry environment)
make demo      # build and bisect the demo incident
make demo-kind # the same incident in a real kind cluster (needs Docker + kind)
```

## Pull requests

- Keep changes focused, and add tests for new behaviour. Sources should be testable from recorded tool output, without a cluster.
- Run `go vet ./...` and `gofmt`.
- Use conventional commit prefixes (`feat:`, `fix:`, `docs:`, `test:`). The changelog is generated from them.
- New configuration fields belong in [docs/configuration.md](docs/configuration.md).

## Good first issues

- An `argocd` source (application history via `argocd app history`).
- A `terraform` source for Terraform Cloud / HCP state versions.
- A `registry` source listing image tags with their push times.
- A `kubeconform` check type.
- Parallel probing (test several states at once on several clusters).

See [docs/architecture.md](docs/architecture.md) for how the pieces fit together.
