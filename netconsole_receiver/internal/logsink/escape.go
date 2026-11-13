// Package logsink writes one-line-per-datagram log files with size-based
// rotation done by the process itself (no external logrotate).
package logsink

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Escape makes s safe for a one-line log record: newline, carriage
// return and tab become \n, \r, \t; every other control character, DEL
// and every invalid UTF-8 byte becomes \xNN. Valid printable UTF-8 is kept.
// Backslashes are deliberately NOT doubled, so the kernel's own "\xNN"
// escaping stays readable and greppable; the log is for people, not parsers.
func Escape(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			sb.WriteString(`\n`)
			i++
		case c == '\r':
			sb.WriteString(`\r`)
			i++
		case c == '\t':
			sb.WriteString(`\t`)
			i++
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, c)
			i++
		case c < 0x7f:
			sb.WriteByte(c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n <= 1 || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
				for j := 0; j < n; j++ {
					fmt.Fprintf(&sb, `\x%02x`, s[i+j])
				}
			} else {
				sb.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return sb.String()
}
