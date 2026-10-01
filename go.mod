// The module path is the import prefix for every package in this repository,
// e.g. github.com/SrijitGyawali/Centralized-Exchange/internal/book.
// It matches the GitHub URL so `go install .../cmd/cexd@latest` works.
module github.com/SrijitGyawali/Centralized-Exchange

// The `go` line pins the minimum language version and, since Go 1.21, the
// toolchain the go command will use. Pinning keeps builds reproducible:
// everyone (you, CI, Docker) compiles with the same compiler and stdlib.
go 1.26.5
