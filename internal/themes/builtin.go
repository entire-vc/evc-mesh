package themes

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

//go:embed builtin
var builtinFS embed.FS

// builtinDir is the directory of an embedded theme: builtin/<id>/.
func builtinDir(id string) string { return "builtin/" + id }

// BuiltinIDs lists the themes an admin can install from the ones shipped with
// Mesh. mesh-default is not among them: it is the app's own look, installed on
// its own the first time settings are saved for it (see Service.Select).
func BuiltinIDs() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != domain.DefaultThemeID {
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

// builtinKnown reports whether an embedded theme with this id exists, mesh-default included.
func builtinKnown(id string) bool {
	_, err := fs.Stat(builtinFS, builtinDir(id))
	return err == nil
}
