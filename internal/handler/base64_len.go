package handler

// base64PaddedLen is the standard padded base64 length of n bytes, kept in
// int64 so a large configured limit is not truncated by an int conversion.
// Callers pass a config-bounded n (see config.maxArtifactBytesLimit).
func base64PaddedLen(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + 2) / 3 * 4
}
