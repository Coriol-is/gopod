package runner

import "strings"

// stripTerminalEscapes removes terminal escape sequences from s:
//
//   - CSI: ESC [ ... final byte in 0x40–0x7E (colors, cursor movement)
//   - OSC: ESC ] ... terminated by BEL or ST (ESC \) — includes OSC-8
//     hyperlinks, whose parameters carry a duplicate of the visible URL
//
// Unterminated sequences swallow the rest of the string, which is the
// safe choice for login-URL extraction: a partial escape is garbage.
func stripTerminalEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[': // CSI
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					j++ // skip the final byte
				}
				i = j
				continue
			case ']': // OSC
				j := i + 2
				for j < len(s) {
					if s[j] == 0x07 { // BEL
						j++
						break
					}
					if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' { // ST
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
