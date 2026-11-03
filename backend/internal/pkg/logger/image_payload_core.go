package logger

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagepayload"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Runs before stdout/file encoders; the Ops sink separately sanitizes its copy.
type imagePayloadCore struct{ zapcore.Core }

func (c imagePayloadCore) With(fields []zapcore.Field) zapcore.Core {
	return imagePayloadCore{c.Core.With(sanitizeImageLogFields(fields))}
}

func (c imagePayloadCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c imagePayloadCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	entry.Message = imagepayload.SanitizeText(entry.Message)
	return c.Core.Write(entry, sanitizeImageLogFields(fields))
}

func sanitizeImageLogFields(fields []zapcore.Field) []zapcore.Field {
	out := make([]zapcore.Field, 0, len(fields))
	for _, field := range fields {
		switch field.Type {
		case zapcore.StringType:
			field.String = imagepayload.SanitizeField(field.Key, field.String)
		case zapcore.ByteStringType:
			if value, ok := field.Interface.([]byte); ok {
				field = zap.String(field.Key, imagepayload.SanitizeField(field.Key, string(value)))
			}
		case zapcore.ErrorType, zapcore.StringerType, zapcore.ReflectType, zapcore.ObjectMarshalerType, zapcore.ArrayMarshalerType, zapcore.InlineMarshalerType, zapcore.BinaryType:
			encoder := zapcore.NewMapObjectEncoder()
			field.AddTo(encoder)
			raw, err := json.Marshal(encoder.Fields)
			if err != nil {
				break
			}
			clean, err := imagepayload.SanitizeJSON(raw)
			if err != nil || bytes.Equal(raw, clean) {
				break
			}
			var values map[string]any
			decoder := json.NewDecoder(bytes.NewReader(clean))
			decoder.UseNumber()
			if decoder.Decode(&values) == nil {
				// Rich errors and inline objects may emit several sibling keys.
				// Keep their other diagnostics, including errorVerbose/errorCauses.
				keys := make([]string, 0, len(values))
				for key := range values {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					// Reflect preserves json.Number as a JSON number; zap.Any can
					// select Stringer and accidentally turn it into a string.
					out = append(out, zap.Reflect(key, values[key]))
				}
				continue
			}
		}
		out = append(out, field)
	}
	return out
}
