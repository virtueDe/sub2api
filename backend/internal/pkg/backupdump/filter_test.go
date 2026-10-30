package backupdump

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func TestFilterPreservesBusinessDataAndRemovesImages(t *testing.T) {
	for _, table := range []string{"usage_logs", `"ops_error_logs"`} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(table+newline, func(t *testing.T) {
				raw := `{"prompt":"猫\nline\t\\path","request_params":{"prompt":"keep","seed":9007199254740993,"nested":[{"image_url":{"url":"data:image/png;base64,YQ=="}}]},"images":[{"b64_json":"output-secret","url":"https://example.test/cat.png","revised_prompt":"a cat","partial":true}],"future":{"cost":0.1234567890123456789}}`
				header := `COPY "public".` + table + ` (id, "image_generation", actual_cost, created_at) FROM stdin;` + newline
				before := header + "41\t" + string(encodeCopy([]byte(raw))) + "\t1.23456789\t2026-10-30 06:00:00+08" + newline + "\\." + newline
				var out bytes.Buffer
				require.NoError(t, Filter(&out, iotest.OneByteReader(strings.NewReader(before))))
				require.NotContains(t, out.String(), "output-secret")
				require.NotContains(t, out.String(), "data:image/")
				lines := strings.Split(out.String(), newline)
				fields := strings.Split(lines[1], "\t")
				require.Equal(t, []string{"41", "1.23456789", "2026-10-30 06:00:00+08"}, []string{fields[0], fields[2], fields[3]})
				decoded, err := decodeCopy([]byte(fields[1]))
				require.NoError(t, err)
				require.JSONEq(t, `{"prompt":"猫\nline\t\\path","request_params":{"prompt":"keep","seed":9007199254740993,"nested":[{"image_url":{"url":"[IMAGE_DATA_OMITTED]"}}]},"images":[{"url":"https://example.test/cat.png","revised_prompt":"a cat","partial":true}],"future":{"cost":0.1234567890123456789}}`, string(decoded))
				require.Contains(t, string(decoded), "9007199254740993")
				require.Contains(t, string(decoded), "0.1234567890123456789")
			})
		}
	}
}

func TestFilterNullsUnchangedAndLegacyColumns(t *testing.T) {
	input := "COPY public.usage_logs (image_generation, id) FROM stdin;\n" +
		"\\N\t1\nnull\t2\n{}\t3\n{\"prompt\": \"keep\"}\t4\n\\.\n" +
		"COPY public.usage_logs (id, model) FROM stdin;\n5\tlegacy\n\\.\n" +
		"COPY public.other (id, image_generation) FROM stdin;\n6\t{\"images\":[{\"b64_json\":\"keep\"}]}\n\\.\n"
	var out bytes.Buffer
	require.NoError(t, Filter(&out, strings.NewReader(input)))
	require.Equal(t, input, out.String())
}

func TestFilterImageAtEndAndBufferBoundaries(t *testing.T) {
	other := strings.Repeat("business-data", 20000)
	raw := `{"prompt":"keep","images":[{"b64_json":"` + strings.Repeat("YQ==", 300000) + `"}]}`
	input := "COPY public.usage_logs (business, image_generation) FROM stdin;\r\n" + other + "\t" + raw + "\r\n\\.\r\n"
	var out bytes.Buffer
	require.NoError(t, Filter(&out, strings.NewReader(input)))
	require.Equal(t, "COPY public.usage_logs (business, image_generation) FROM stdin;\r\n"+other+"\t"+`{"images":[{}],"prompt":"keep"}`+"\r\n\\.\r\n", out.String())
	// Non-target COPY rows do not have a line-length limit either.
	input = "COPY public.notes (body) FROM stdin;\n" + other + "\n\\.\n"
	out.Reset()
	require.NoError(t, Filter(&out, strings.NewReader(input)))
	require.Equal(t, input, out.String())
}

func TestFilterDoesNotInterpretSQLBodiesAsCopy(t *testing.T) {
	for _, wrapper := range []string{
		"CREATE FUNCTION example() RETURNS text AS $body$\n%s\n$body$ LANGUAGE sql;\n",
		"COMMENT ON TABLE example IS '\n%s\n';\n",
		"COMMENT ON TABLE example IS E'escaped\\'\n%s\n';\n",
		"/* nested /* comment */\n%s\n*/\n",
	} {
		fake := "COPY public.usage_logs (image_generation) FROM stdin;\ninvalid-json\n\\."
		input := strings.Replace(wrapper, "%s", fake, 1) + "COPY public.usage_logs (image_generation) FROM stdin;\n" + `{"images":[{"b64_json":"remove"}]}` + "\n\\.\n"
		var out bytes.Buffer
		require.NoError(t, Filter(&out, strings.NewReader(input)))
		require.True(t, strings.HasPrefix(out.String(), strings.Replace(wrapper, "%s", fake, 1)))
		require.NotContains(t, out.String(), "remove")
	}
}

func TestFilterRejectsDamagedCopyWithoutPayloadInErrors(t *testing.T) {
	for _, body := range []string{
		"1\tSECRET-not-json\n\\.\n",
		"1\t{}\textra\n\\.\n",
		"1\n\\.\n",
		"1\t{}\n",
		"1\t{}",
		"1\t\\q\n\\.\n",
	} {
		err := Filter(io.Discard, strings.NewReader("COPY public.usage_logs (id, image_generation) FROM stdin;\n"+body))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "SECRET")
	}
}

func TestFilterPropagatesStreamErrors(t *testing.T) {
	boom := errors.New("transport failed")
	require.ErrorIs(t, Filter(io.Discard, iotest.ErrReader(boom)), boom)
	require.ErrorIs(t, Filter(errorWriter{boom}, strings.NewReader("SELECT 1;\n")), boom)
	input := "COPY public.usage_logs (image_generation) FROM stdin;\n{}\n"
	require.ErrorIs(t, Filter(io.Discard, io.MultiReader(strings.NewReader(input), iotest.ErrReader(boom))), boom)
}

func TestFilterRejectsOversizedFieldsAndSQL(t *testing.T) {
	t.Run("image field", func(t *testing.T) {
		src := io.MultiReader(strings.NewReader("COPY public.usage_logs (image_generation) FROM stdin;\n"),
			io.LimitReader(repeatedByte('x'), maxImageField+1))
		err := Filter(io.Discard, src)
		require.ErrorContains(t, err, "64 MiB limit")
	})
	t.Run("SQL line", func(t *testing.T) {
		err := Filter(io.Discard, io.LimitReader(repeatedByte('x'), maxSQLLine+1))
		require.ErrorContains(t, err, "SQL line exceeds size limit")
	})
}

func TestCopyEscapesRoundTrip(t *testing.T) {
	for _, text := range []string{"猫\nline\t\\N\r\b\f\v", "\\.", "\\N", "quotes\"'"} {
		raw, err := json.Marshal(map[string]string{"prompt": text})
		require.NoError(t, err)
		decoded, err := decodeCopy(encodeCopy(raw))
		require.NoError(t, err)
		require.Equal(t, raw, decoded)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type repeatedByte byte

func (b repeatedByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
