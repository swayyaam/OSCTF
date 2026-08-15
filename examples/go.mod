// The example challenges are independent programs — each is built into its own challenge image,
// not linked into the platform binary. This nested go.mod keeps them OUT of the platform module
// so `go build ./...` / `go test ./...` / lint at the repo root stay scoped to the platform,
// exactly as when the module lived under api/. They are stdlib-only, so there is no require block.
module github.com/osctf/platform/examples

go 1.25.7
