// Package webui holds the browser interface shared by gofbpsrvls and
// gofbpstore: plain HTML, CSS and JavaScript, embedded into the binary.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Files returns the files of the interface, index.html at the root.
func Files() fs.FS {
	files, _ := fs.Sub(static, "static")
	return files
}
