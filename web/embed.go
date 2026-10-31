// Package web embeds the Vite build into the Go binary. Build the UI first
// (`npm --prefix web run build`), or create an empty web/dist/.keep for Go-only work.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
