package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"syscall"
)

var (
	imageOffloadIndexPattern = regexp.MustCompile(`^image ([0-9]+):`)
	imageOffloadURLPattern   = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	imageOffloadIPPattern    = regexp.MustCompile(`(?i)(?:\[[0-9a-f:.%]+\]|(?:[0-9]{1,3}\.){3}[0-9]{1,3})(?::[0-9]+)?`)
)

func imageTaskOffloadFailureMessage(err error) string {
	if err == nil {
		return "Generated image offload failed"
	}

	rawMessage := err.Error()
	imageIndex := -1
	if matches := imageOffloadIndexPattern.FindStringSubmatch(rawMessage); len(matches) == 2 {
		if _, scanErr := fmt.Sscanf(matches[1], "%d", &imageIndex); scanErr != nil {
			imageIndex = -1
		}
	}
	stage := imageTaskOffloadFailureStage(rawMessage)
	reason := imageTaskOffloadFailureReason(err)
	if reason == "" {
		reason = sanitizeImageOffloadErrorDetail(rawMessage, imageIndex)
	}

	message := "Generated image offload failed"
	if imageIndex >= 0 {
		message += fmt.Sprintf(" for image %d", imageIndex)
	}
	if stage != "" {
		message += " during " + stage
	}
	if reason != "" {
		message += ": " + reason
	}
	return message
}

func imageTaskOffloadFailureStage(message string) string {
	switch {
	case strings.Contains(message, "download image:") || strings.Contains(message, "build download request:"):
		return "download"
	case strings.Contains(message, "read image body:"):
		return "response read"
	case strings.Contains(message, "upload to object storage:") || strings.Contains(message, "S3 PutObject:"):
		return "object storage upload"
	case strings.Contains(message, "presign url:"):
		return "object storage URL generation"
	case strings.Contains(message, "decode b64_json") || strings.Contains(message, "decode image data URL"):
		return "image data decoding"
	case strings.Contains(message, "parse image response"):
		return "upstream response parsing"
	case strings.Contains(message, "encode image response") || strings.Contains(message, "encode url"):
		return "response encoding"
	default:
		return "image processing"
	}
}

func imageTaskOffloadFailureReason(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout awaiting response headers") || strings.Contains(message, "awaiting headers") {
		return "timed out waiting for response headers"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request deadline exceeded"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return "DNS lookup timed out"
		}
		if dnsErr.Err != "" {
			return "DNS lookup failed: " + sanitizeImageOffloadErrorDetail(dnsErr.Err, -1)
		}
		return "DNS lookup failed"
	}
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset by peer"
	case errors.Is(err, syscall.ECONNABORTED):
		return "connection aborted"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "connection timed out"
	case errors.Is(err, syscall.EPIPE):
		return "connection closed unexpectedly"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed before the response completed"
	case errors.Is(err, io.EOF):
		return "connection closed before the response started"
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) && netErr.Timeout() {
		switch netErr.Op {
		case "dial":
			return "connection timed out"
		case "read":
			return "timed out reading the response"
		default:
			return "network operation timed out"
		}
	}
	return ""
}

func sanitizeImageOffloadErrorDetail(message string, imageIndex int) string {
	if imageIndex >= 0 {
		message = strings.TrimPrefix(message, fmt.Sprintf("image %d: ", imageIndex))
	}
	for _, prefix := range []string{
		"download image: ",
		"build download request: ",
		"read image body: ",
		"upload to object storage: ",
		"S3 PutObject: ",
		"presign url: ",
	} {
		if index := strings.Index(message, prefix); index >= 0 {
			message = message[index+len(prefix):]
			break
		}
	}
	message = imageOffloadURLPattern.ReplaceAllString(message, "[redacted URL]")
	message = imageOffloadIPPattern.ReplaceAllString(message, "[redacted address]")
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		message = string([]rune(message)[:512])
	}
	return message
}
