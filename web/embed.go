// Package web holds the single-page view served by "disco serve".
package web

import "embed"

//go:embed index.html
var FS embed.FS
