package main

import _ "embed"

// The dashboard is one self-contained page: no build step, no npm, no separate
// frontend server and no assets to deploy.
//
// It lives in dashboard/index.html as real HTML/CSS/JS rather than as a Go
// string literal, so it can be edited, diffed and reviewed like normal source.
// The file is compiled into the binary by go:embed, so `go build` produces a
// binary that still needs nothing on disk at runtime.
//
//go:embed dashboard/index.html
var dashboardHTML string
