package themes

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin
var builtinFS embed.FS

// builtinDir is the directory of an embedded theme: builtin/<id>/.
func builtinDir(id string) string { return "builtin/" + id }

// BuiltinIDs lists the themes shipped with Mesh. mesh-default is not among
// them: it is the app's own look, not an installable extension.
func BuiltinIDs() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// builtinFile reads a file of an embedded theme.
func builtinFile(id, path string) ([]byte, error) {
	b, err := builtinFS.ReadFile(builtinDir(id) + "/" + path)
	if err != nil {
		return nil, fmt.Errorf("builtin theme %q has no file %q", id, path)
	}
	return b, nil
}
