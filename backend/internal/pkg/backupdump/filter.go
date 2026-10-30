// Package backupdump filters image payloads from pg_dump's text COPY streams.
// It never changes the source database or the surrounding restore SQL.
package backupdump

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagepayload"
)

const (
	maxSQLLine = 1 << 20
	// Bound per-field allocations before JSON decoding. Oversized or malformed
	// fields fail the backup rather than retaining images or exhausting memory.
	maxImageField = 64 << 20
)

var targetCopy = regexp.MustCompile(`^COPY (?:public|"public")\.(?:"(usage_logs|ops_error_logs)"|(usage_logs|ops_error_logs)) \((.*)\) FROM stdin;$`)

// Filter preserves one pg_dump snapshot and the plain SQL restore format. Only
// image_generation in the two image log tables is transformed. Other columns,
// tables, schema, sequences and function bodies pass through unchanged.
func Filter(dst io.Writer, src io.Reader) error {
	r := bufio.NewReaderSize(src, 64<<10)
	var lex sqlQuotes
	for {
		line, err := sqlLine(r)
		if len(line) != 0 {
			header := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			isCopy := lex.neutral() && strings.HasPrefix(header, "COPY ") && strings.HasSuffix(header, " FROM stdin;")
			if _, writeErr := dst.Write(line); writeErr != nil {
				return writeErr
			}
			if isCopy {
				column, count := -1, 0
				if match := targetCopy.FindStringSubmatch(header); match != nil {
					columns := strings.Split(match[3], ",")
					count = len(columns)
					for i, name := range columns {
						name = strings.TrimSpace(name)
						if name == "image_generation" || name == `"image_generation"` {
							column = i
						}
					}
				}
				if copyErr := copyRows(dst, r, column, count); copyErr != nil {
					return fmt.Errorf("filter backup COPY data: %w", copyErr)
				}
			} else {
				lex.feed(line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func sqlLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxSQLLine {
			return nil, errors.New("backup SQL line exceeds size limit")
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func copyRows(dst io.Writer, r *bufio.Reader, imageColumn, columns int) error {
	var field []byte
	for {
		part, err := r.ReadSlice('\n')
		if bytes.Equal(part, []byte("\\.\n")) || bytes.Equal(part, []byte("\\.\r\n")) {
			_, err = dst.Write(part)
			return err
		}
		column := 0
		for {
			if imageColumn < 0 {
				if _, writeErr := dst.Write(part); writeErr != nil {
					return writeErr
				}
			} else {
				for len(part) > 0 {
					i := bytes.IndexAny(part, "\t\n")
					n := len(part)
					if i >= 0 {
						n = i
					}
					if column == imageColumn {
						if len(field)+n > maxImageField {
							return errors.New("image_generation COPY field exceeds 64 MiB limit")
						}
						field = append(field, part[:n]...)
					} else if _, writeErr := dst.Write(part[:n]); writeErr != nil {
						return writeErr
					}
					part = part[n:]
					if i < 0 {
						break
					}
					delimiter := part[0]
					if column == imageColumn {
						crlf := delimiter == '\n' && bytes.HasSuffix(field, []byte{'\r'})
						if crlf {
							field = field[:len(field)-1]
						}
						if cleanErr := writeImageField(dst, field); cleanErr != nil {
							return cleanErr
						}
						field = field[:0]
						if crlf {
							if _, writeErr := dst.Write([]byte{'\r'}); writeErr != nil {
								return writeErr
							}
						}
					}
					column++
					if delimiter == '\n' && column != columns {
						return errors.New("unexpected image log COPY column count")
					}
					if _, writeErr := dst.Write(part[:1]); writeErr != nil {
						return writeErr
					}
					part = part[1:]
				}
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				if err != nil {
					if errors.Is(err, io.EOF) {
						return errors.New("truncated backup COPY data")
					}
					return err
				}
				break
			}
			part, err = r.ReadSlice('\n')
		}
	}
}

func writeImageField(dst io.Writer, raw []byte) error {
	if bytes.Equal(raw, []byte(`\N`)) {
		_, err := dst.Write(raw)
		return err
	}
	decoded, err := decodeCopy(raw)
	if err != nil {
		return err
	}
	clean, _, changed, err := imagepayload.CleanDetail(decoded, true)
	if err != nil {
		// Do not include field contents or JSON parse diagnostics in backup logs.
		return errors.New("invalid image_generation JSON in backup COPY data")
	}
	if !changed {
		_, err = dst.Write(raw)
		return err
	}
	_, err = dst.Write(encodeCopy(clean))
	return err
}

func decodeCopy(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i == len(raw) {
			return nil, errors.New("truncated COPY escape")
		}
		c = raw[i]
		switch c {
		case 'b':
			c = '\b'
		case 'f':
			c = '\f'
		case 'n':
			c = '\n'
		case 'r':
			c = '\r'
		case 't':
			c = '\t'
		case 'v':
			c = '\v'
		case '\\':
		default:
			// pg_dump/COPY TO emits the escapes above, not octal/hex forms.
			return nil, errors.New("unsupported image_generation COPY escape")
		}
		out = append(out, c)
	}
	return out, nil
}

func encodeCopy(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for _, c := range raw {
		switch c {
		case '\\':
			out = append(out, '\\', '\\')
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		case '\v':
			out = append(out, '\\', 'v')
		default:
			out = append(out, c)
		}
	}
	return out
}

// Track SQL quoting so COPY-looking text in function bodies, comments or string
// literals is never interpreted as table data. COPY rows bypass this lexer.
type sqlQuotes struct {
	quote   byte
	escape  bool
	dollar  string
	comment int
}

func (s *sqlQuotes) neutral() bool { return s.quote == 0 && s.dollar == "" && s.comment == 0 }

func (s *sqlQuotes) feed(line []byte) {
	for i := 0; i < len(line); i++ {
		c := line[i]
		if s.dollar != "" {
			if bytes.HasPrefix(line[i:], []byte(s.dollar)) {
				i += len(s.dollar) - 1
				s.dollar = ""
			}
			continue
		}
		if s.comment > 0 {
			if bytes.HasPrefix(line[i:], []byte("/*")) {
				s.comment++
				i++
			} else if bytes.HasPrefix(line[i:], []byte("*/")) {
				s.comment--
				i++
			}
			continue
		}
		if s.quote != 0 {
			if c == '\\' && s.escape {
				i++
			} else if c == s.quote {
				if i+1 < len(line) && line[i+1] == s.quote {
					i++
				} else {
					s.quote = 0
				}
			}
			continue
		}
		if bytes.HasPrefix(line[i:], []byte("--")) {
			return
		}
		if bytes.HasPrefix(line[i:], []byte("/*")) {
			s.comment = 1
			i++
			continue
		}
		switch c {
		case '\'', '"':
			s.quote = c
			s.escape = c == '\'' && i > 0 && (line[i-1] == 'E' || line[i-1] == 'e')
		case '$':
			j := i + 1
			for j < len(line) && (line[j] == '_' || line[j] >= 'a' && line[j] <= 'z' || line[j] >= 'A' && line[j] <= 'Z' || j > i+1 && line[j] >= '0' && line[j] <= '9') {
				j++
			}
			if j < len(line) && line[j] == '$' {
				s.dollar = string(line[i : j+1])
				i = j
			}
		}
	}
}
