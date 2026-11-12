package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageMultipartRewritePreservesPartsAndNoop(t *testing.T) {
	var raw bytes.Buffer
	w := multipart.NewWriter(&raw)
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("prompt", "original"))
	require.NoError(t, w.WriteField("aspect_ratio", "16:9"))
	require.NoError(t, w.WriteField("unknown", "keep me"))
	for _, name := range []string{"image[]", "mask", "markup", "unknown_file"} {
		p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="` + name + `"; filename="test.png"`}, "Content-Type": {"image/png"}, "Content-Transfer-Encoding": {"quoted-printable"}, "X-Custom": {"keep"}})
		require.NoError(t, err)
		_, err = io.WriteString(p, "a=3Db\r\n--not-the-boundary")
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	body, ct := raw.Bytes(), w.FormDataContentType()
	same, sameCT, err := rewriteOpenAIImagesMultipartModel(body, ct, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, ct, sameCT)
	require.True(t, &same[0] == &body[0])
	model, prompt, format := "gpt-image-1", "changed", "b64_json"
	out, outCT, err := rewriteOpenAIImagesMultipartFields(body, ct, map[string]*string{"model": &model, "prompt": &prompt, "response_format": &format, "aspect_ratio": nil})
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(outCT)
	require.NoError(t, err)
	r := multipart.NewReader(bytes.NewReader(out), params["boundary"])
	fields := map[string][]string{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(p)
		require.NoError(t, err)
		if p.FileName() != "" {
			require.Equal(t, "a=b\r\n--not-the-boundary", string(data))
			require.Equal(t, "keep", p.Header.Get("X-Custom"))
		}
		fields[p.FormName()] = append(fields[p.FormName()], string(data))
	}
	require.Equal(t, []string{model, model}, fields["model"])
	require.Equal(t, []string{prompt}, fields["prompt"])
	require.Equal(t, []string{format}, fields["response_format"])
	require.Equal(t, []string{"keep me"}, fields["unknown"])
	require.NotContains(t, fields, "aspect_ratio")
	parsed, err := parseImagesEditMultipart(t, body, ct)
	require.NoError(t, err)
	require.Nil(t, parsed.Uploads[0].Data)
	dataURL, err := parsed.Uploads[0].DataURL()
	require.NoError(t, err)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("a=b\r\n--not-the-boundary")), dataURL)
	require.NotNil(t, parsed.MaskUpload)
	require.NotNil(t, parsed.MarkupUpload)
	var encoder ImageUploadEncoder
	for _, upload := range []OpenAIImagesUpload{parsed.Uploads[0], *parsed.MarkupUpload, *parsed.MaskUpload, parsed.Uploads[0]} {
		want, err := upload.DataURL()
		require.NoError(t, err)
		got, err := encoder.DataURL(upload)
		require.NoError(t, err)
		require.Equal(t, want, got, "backwards mask/markup references must preserve independent-reader behavior")
	}
}

type imageEncodingCountingReader struct {
	io.Reader
	read int
}

func (r *imageEncodingCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func multipartEncodingFixture(t testing.TB, count, total int, quoted bool) ([]byte, string, []OpenAIImagesUpload) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("prompt", "keep"))
	data := bytes.Repeat([]byte{0xab}, total/count)
	for range count {
		header := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="image[]"; filename="test.png"`}, "Content-Type": {"image/png"}}
		if quoted {
			header.Set("Content-Transfer-Encoding", "quoted-printable")
		}
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		if quoted {
			q := quotedprintable.NewWriter(part)
			_, err = q.Write(data)
			require.NoError(t, err)
			require.NoError(t, q.Close())
		} else {
			_, err = part.Write(data)
			require.NoError(t, err)
		}
	}
	require.NoError(t, w.Close())
	var parsed OpenAIImagesRequest
	require.NoError(t, parseOpenAIImagesMultipartRequestMode(body.Bytes(), w.FormDataContentType(), &parsed, false))
	return body.Bytes(), w.Boundary(), parsed.Uploads
}

func TestImageUploadEncoderReadsMultipartOnce(t *testing.T) {
	for _, quoted := range []bool{false, true} {
		body, boundary, uploads := multipartEncodingFixture(t, 64, 1<<20, quoted)
		counter := &imageEncodingCountingReader{Reader: bytes.NewReader(body)}
		encoder := ImageUploadEncoder{body: body, boundary: boundary, reader: multipart.NewReader(counter, boundary)}
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, (1<<20)/64))
		for _, upload := range uploads {
			require.Nil(t, upload.Data)
			got, err := encoder.DataURL(upload)
			require.NoError(t, err)
			require.Equal(t, want, got)
		}
		require.GreaterOrEqual(t, counter.read, len(body)-4096, "ordered uploads must use the same MIME reader")
		require.LessOrEqual(t, counter.read, len(body), "encoding must not reread the body for every image")
	}
}

func BenchmarkImageUploadEncoder(b *testing.B) {
	for _, quoted := range []bool{false, true} {
		for _, count := range []int{4, 16, 64} {
			b.Run(fmt.Sprintf("qp=%v/images=%d", quoted, count), func(b *testing.B) {
				body, _, uploads := multipartEncodingFixture(b, count, 4<<20, quoted)
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var encoder ImageUploadEncoder
					for _, upload := range uploads {
						if _, err := encoder.DataURL(upload); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func BenchmarkImageMultipartUnchanged(b *testing.B) {
	for _, mib := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			benchmarkImageMultipartUnchanged(b, mib<<20)
		})
	}
}

func benchmarkImageMultipartUnchanged(b *testing.B, size int) {
	var raw bytes.Buffer
	w := multipart.NewWriter(&raw)
	_ = w.WriteField("model", "gpt-image-2")
	p, _ := w.CreateFormFile("image", "test.png")
	_, _ = p.Write(bytes.Repeat([]byte{'x'}, size))
	_ = w.Close()
	body, ct := raw.Bytes(), w.FormDataContentType()
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, _, err := rewriteOpenAIImagesMultipartModel(body, ct, "gpt-image-2")
		if err != nil || &out[0] != &body[0] {
			b.Fatal("no-op changed request", err)
		}
	}
}
