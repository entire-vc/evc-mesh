package extensions

// systemParts are installed extensions that are internal pieces of another
// product rather than something an administrator manages by hand: the server
// side of the runner is managed from the Runner section, not from the
// extension lists. They stay installed and working; screens leave them out.
var systemParts = map[string]bool{
	"runner": true,
}

// IsSystemPart reports whether the extension id is such an internal piece.
func IsSystemPart(id string) bool {
	return systemParts[id]
}
