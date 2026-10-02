package jsonstream

import (
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// tokError is a tokenizer-level syntax error.
type tokError struct {
	off   int64
	trunc bool
	msg   string
}

func (e *tokError) Error() string { return e.msg }

// decodeJSONString decodes the raw (unquoted) contents of a JSON string,
// including escape sequences.
func decodeJSONString(s string) (string, error) {
	var b []byte
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\\' {
			i++
			if i >= len(s) {
				return "", errors.New("jsonstream: unterminated escape in string")
			}
			esc := s[i]
			i++
			switch esc {
			case '"':
				b = append(b, '"')
			case '\\':
				b = append(b, '\\')
			case '/':
				b = append(b, '/')
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				r, n, err := decodeUnicodeEscape(s, i)
				if err != nil {
					return "", err
				}
				i = n
				if utf16.IsSurrogate(r) {
					// expect immediately following \uXXXX low surrogate
					if i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
						r2, n2, err := decodeUnicodeEscape(s, i+2)
						if err != nil {
							return "", err
						}
						if r2 < 0xDC00 || r2 > 0xDFFF {
							return "", errors.New("jsonstream: invalid surrogate pair in string")
						}
						r = 0x10000 + (r-0xD800)*0x400 + (r2 - 0xDC00)
						i = n2
					} else {
						return "", errors.New("jsonstream: unpaired high surrogate in string")
					}
				} else if r >= 0xDC00 && r <= 0xDFFF {
					return "", errors.New("jsonstream: unexpected low surrogate in string")
				}
				var buf [4]byte
				n3 := utf8.EncodeRune(buf[:], r)
				b = append(b, buf[:n3]...)
			default:
				return "", errors.New("jsonstream: invalid escape \\" + string(esc) + " in string")
			}
			continue
		}
		if c < 0x20 {
			return "", errors.New("jsonstream: unescaped control character in string")
		}
		b = append(b, c)
		i++
	}
	return string(b), nil
}

func decodeUnicodeEscape(s string, i int) (rune, int, error) {
	if i+4 > len(s) {
		return 0, i, errors.New("jsonstream: truncated unicode escape in string")
	}
	var v rune
	for k := 0; k < 4; k++ {
		c := s[i+k]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, i, errors.New("jsonstream: invalid unicode escape in string")
		}
		v = v*16 + rune(d)
	}
	return v, i + 4, nil
}
