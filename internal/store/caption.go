package store

import "unicode/utf8"

// MaxUploadCaptionUnits is Telegram's caption limit. Telegram counts UTF-16
// code units after entity parsing, so a rune count or a byte count is not the
// same number: an emoji is one rune, four bytes and two units.
const MaxUploadCaptionUnits = 1024

// CaptionUTF16Units returns the number of UTF-16 code units in s. An invalid
// byte counts as one unit (U+FFFD).
func CaptionUTF16Units(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
