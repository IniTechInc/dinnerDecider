//go:build tools

// Package tools pins the module's direct dependencies so `go mod tidy` cannot
// drop them before the packages that consume them exist.
//
// The Linux port declares every dependency once, in the foundation task, and
// freezes go.mod thereafter (implementation plan, §1 and §1.1). Later tasks
// import these for real:
//
//	modernc.org/sqlite                      -> internal/store      (pure-Go SQLite, no cgo)
//	golang.org/x/image/draw, .../webp       -> internal/vision     (resize, WebP decode)
//	golang.org/x/sys/unix                   -> internal/llm, model (Statfs)
//	github.com/santhosh-tekuri/jsonschema/v6 -> tests only         (shared/schema validation)
//
// This file is excluded from every real build by its `tools` constraint, but
// `go mod tidy` reads it under all build tags, which is exactly the point.
package tools

import (
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)
