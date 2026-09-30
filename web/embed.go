// Package web holds the demo's single-page UI, embedded into the demoui binary
// so the demo runs with `go run` and no build step.
package web

import "embed"

//go:embed index.html app.js styles.css
var Files embed.FS
