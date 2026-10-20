// Not a Go package — this file exists only to keep `go build ./...` out of
// node_modules.
//
// npm packages sometimes ship Go source (eslint pulls in `flatted`, which
// carries a Go port), and cmd/go walks every directory under the module root.
// A nested go.mod marks this subtree as a different module, which is what
// makes the parent's ./... skip it. Nothing here is ever imported or built.
module zcribe-ui-not-a-go-module

go 1.23
