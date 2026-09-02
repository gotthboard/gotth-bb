package httpui

import "fmt"

// parseTopicPageQuery accepts only the area-list query contract's literal
// canonical spelling. It reads RawQuery rather than url.Values so duplicate,
// encoded, reordered, or silently discarded input cannot acquire meaning.
//
// Complexity: time is O(min(q,15)), Theta(1) for absent input, and Omega(1),
// where q is raw query bytes. Auxiliary space is Theta(1).
func parseTopicPageQuery(rawQuery string, maximum int32) (int32, error) {
	if maximum < 1 {
		return 0, fmt.Errorf("topic page maximum must be positive")
	}
	if rawQuery == "" {
		return 1, nil
	}
	if len(rawQuery) < len("page=1") || len(rawQuery) > len("page=2147483647") || rawQuery[:len("page=")] != "page=" {
		return 0, fmt.Errorf("topic page query is invalid")
	}
	digits := rawQuery[len("page="):]
	if digits[0] == '0' {
		return 0, fmt.Errorf("topic page query is invalid")
	}
	var page int32
	for index := 0; index < len(digits); index++ {
		digit := digits[index]
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("topic page query is invalid")
		}
		next := int64(page)*10 + int64(digit-'0')
		if next > int64(maximum) {
			return 0, fmt.Errorf("topic page query is invalid")
		}
		page = int32(next)
	}
	return page, nil
}
