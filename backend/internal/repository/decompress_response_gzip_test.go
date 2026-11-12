package repository

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type gzipFailureTestBody struct {
	io.Reader
	closes int
}

func (b *gzipFailureTestBody) Close() error {
	b.closes++
	return nil
}

func TestDecompressGzipFailureIsReadError(t *testing.T) {
	valid := compressGzip(t, []byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\"}\n\n"))
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-8] ^= 0xff
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "empty"},
		{name: "plain_sse", body: []byte("event: image_generation.completed\ndata: {\"b64_json\":\"AA==\"}\n\n")},
		{name: "truncated_header", body: []byte{0x1f, 0x8b}},
		{name: "truncated_trailer", body: valid[:len(valid)-4]},
		{name: "invalid_checksum", body: corrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := &gzipFailureTestBody{Reader: bytes.NewReader(tc.body)}
			resp := newEncodedResponse("gzip", tc.body)
			resp.Body = original
			decompressResponseBody(resp)
			_, err := io.ReadAll(resp.Body)
			require.Error(t, err, "bad compression must not become successful EOF")
			require.NotEqual(t, io.EOF, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, 1, original.closes)
		})
	}
}
