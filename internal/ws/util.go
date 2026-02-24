package ws

import "github.com/google/uuid"

// parseUUID parses a UUID string, returning an error if it is invalid.
func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}
