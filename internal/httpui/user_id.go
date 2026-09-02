package httpui

// parseUserID applies the same canonical positive signed-64-bit identifier
// grammar as topic routes.
//
// Complexity: for n input bytes, time is O(min(n,19)), Omega(1), and auxiliary
// space is tight Theta(1).
func parseUserID(raw string) (int64, error) {
	return parseTopicID(raw)
}
