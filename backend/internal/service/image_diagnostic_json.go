package service

import "github.com/tidwall/gjson"

// walkImageDiagnosticJSON visits direct raw children without decoding values.
// In particular, a huge escaped base64 string is skipped with constant memory.
// The request parser owns validation; malformed input simply ends diagnostics.
func walkImageDiagnosticJSON(raw string, visit func(key, value string) bool) {
	object := len(raw) > 0 && raw[0] == '{'
	for pos := 1; pos < len(raw); {
		for pos < len(raw) && (raw[pos] <= ' ' || raw[pos] == ',') {
			pos++
		}
		if pos >= len(raw) || raw[pos] == '}' || raw[pos] == ']' {
			return
		}
		key := ""
		if object {
			if raw[pos] != '"' {
				return
			}
			end := imageDiagnosticTokenEnd(raw, pos)
			if end <= pos {
				return
			}
			if end-pos <= 128 {
				key = gjson.Parse(raw[pos:end]).Str
			}
			pos = end
			for pos < len(raw) && raw[pos] <= ' ' {
				pos++
			}
			if pos >= len(raw) || raw[pos] != ':' {
				return
			}
			pos++
			for pos < len(raw) && raw[pos] <= ' ' {
				pos++
			}
		}
		end := imageDiagnosticTokenEnd(raw, pos)
		if end <= pos || !visit(key, raw[pos:end]) {
			return
		}
		pos = end
	}
}

// Find a complete token using indices only, including nested containers.
func imageDiagnosticTokenEnd(raw string, start int) int {
	depth, quoted := 0, false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if quoted {
			switch ch {
			case '\\':
				i++
			case '"':
				quoted = false
				if depth == 0 {
					return i + 1
				}
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
			if depth == 0 {
				return i + 1
			}
		case ',', ' ', '\t', '\r', '\n':
			if depth == 0 {
				return i
			}
		}
	}
	return start // incomplete token: do not retain it
}
