# Storage engine spike

Throwaway module used by Gate 0 of the
[`make-vfs-mutations-recoverable`](../../../openspec/changes/make-vfs-mutations-recoverable/proposal.md)
change to evaluate `modernc.org/sqlite` as the embedded state engine before any
production dependency or wiring is added.

It is a separate Go module so the main module neither depends on the candidate
engine nor pays its build cost. Run it directly:

```sh
cd scripts/dev/storage-spike
go test -count=1 -v ./...
go test -run '^$' -bench . -benchtime 100x ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ./bin/spike-size ./cmd/spike-size
```

`.github/workflows/storage-spike.yaml` runs the same tests on the Gate 0
platform matrix: Linux amd64, Windows amd64, macOS arm64, plus a best-effort
darwin/amd64 run under Rosetta when the runner has it installed. Android and
iOS are deferred by the maintainer and not covered here.

Results and the accepted platform scope are recorded in
[`gate0-storage-spike.md`](../../../openspec/changes/make-vfs-mutations-recoverable/evidence/gate0-storage-spike.md).
