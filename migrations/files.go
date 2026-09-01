package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var embedded embed.FS

// Files returns the immutable SQL files compiled into the release artifact.
//
// Complexity: time and auxiliary space are O(1), Omega(1), and tight Theta(1).
func Files() fs.FS {
	return embedded
}
