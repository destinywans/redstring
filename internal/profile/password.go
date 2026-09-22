package profile

import (
	"unicode"
	"unicode/utf8"
)

// Password length is measured in Unicode characters, matching the profile form.
// Existing passwords are checked unchanged; this policy only applies to new ones.
func validNewPassword(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	size := utf8.RuneCountInString(value)
	if size < 8 || size > 128 {
		return false
	}
	var upper, lower, digit, symbol bool
	for _, c := range value {
		upper = upper || c >= 'A' && c <= 'Z'
		lower = lower || c >= 'a' && c <= 'z'
		digit = digit || c >= '0' && c <= '9'
		symbol = symbol || unicode.IsPunct(c) || unicode.IsSymbol(c)
	}
	return upper && lower && digit && symbol
}
