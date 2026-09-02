package policy

// ValidAreaSlug reports whether one slug exactly matches the immutable ASCII
// grammar enforced by the areas table: 1-80 lowercase letters or digits,
// separated only by single hyphens.
//
// Complexity: for s slug bytes, time is O(s), Omega(1), and tight Theta(s) for
// valid input because every byte is examined. Auxiliary space is tight
// Theta(1). No normalization or allocation occurs.
func ValidAreaSlug(slug string) bool {
	valid := len(slug) >= 1 && len(slug) <= 80
	for index := 0; valid && index < len(slug); index++ {
		character := slug[index]
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		if character != '-' || index == 0 || index == len(slug)-1 || slug[index-1] == '-' {
			valid = false
		}
	}
	return valid
}
