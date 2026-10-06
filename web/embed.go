// Package web embeds the static assets served by the application.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embedded embed.FS

// Static is the embedded static asset tree (css/, js/, ...), rooted at static/.
var Static = func() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // impossible: "static" is embedded above
	}
	return sub
}()
