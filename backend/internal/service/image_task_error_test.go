package service

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageTaskOffloadFailureMessageClassifiesAndRedactsCauses(t *testing.T) {
	const signedURL = "https://images.example.test/generated.png?X-Amz-Signature=private-signature"
	tests := []struct {
		name       string
		err        error
		want       string
		unwantText []string
	}{
		{
			name:       "response header timeout",
			err:        fmt.Errorf("image 0: download image: Get %q: net/http: timeout awaiting response headers", signedURL),
			want:       "for image 0 during download: timed out waiting for response headers",
			unwantText: []string{"private-signature", "images.example.test"},
		},
		{
			name:       "DNS lookup error",
			err:        fmt.Errorf("image 1: download image: %w", &net.DNSError{Err: "no such host while requesting " + signedURL, Name: "private-upstream.example.test"}),
			want:       "for image 1 during download: DNS lookup failed",
			unwantText: []string{"private-signature", "images.example.test", "private-upstream.example.test"},
		},
		{
			name:       "connection reset",
			err:        fmt.Errorf("image 2: download image: Get %q: %w", signedURL, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}),
			want:       "for image 2 during download: connection reset by peer",
			unwantText: []string{"private-signature", "images.example.test"},
		},
		{
			name: "download HTTP status",
			err:  errors.New("image 3: download image: unexpected status 403"),
			want: "for image 3 during download: unexpected status 403",
		},
		{
			name:       "unsupported URL scheme",
			err:        errors.New(`image 4: download image: unsupported protocol ftp://images.example.test/file?signature=private-signature`),
			want:       "for image 4 during download: unsupported protocol",
			unwantText: []string{"private-signature", "images.example.test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := imageTaskOffloadFailureMessage(tt.err)
			require.Contains(t, message, tt.want)
			for _, text := range tt.unwantText {
				require.NotContains(t, message, text)
			}
		})
	}
}
