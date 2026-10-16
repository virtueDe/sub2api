package imagetiming

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"go.uber.org/zap"
)

type httpKey struct{}

// HTTP measures one logical HTTP call, including its transport callbacks and
// passive body reads. Repeated callbacks (transport retries/redirects) get
// separate spans. Missing hooks, e.g. custom TLS handshakes, are not invented.
func HTTP(req *http.Request, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if req == nil || !Enabled(req.Context()) || req.Context().Value(httpKey{}) != nil {
		return do(req)
	}
	ctx := req.Context()
	call := Start(ctx, "http_headers", zap.String("upstream_host", req.URL.Hostname()),
		zap.String("http_method", req.Method), zap.Int64("request_content_length", req.ContentLength))
	ctx = context.WithValue(call.Context(ctx), httpKey{}, true)
	var mu sync.Mutex
	finished := false
	firstByteSeen := false
	pending := make(map[string][]*Span)
	begin := func(key, stage string) {
		mu.Lock()
		if !finished {
			pending[key] = append(pending[key], Start(ctx, stage))
		}
		mu.Unlock()
	}
	end := func(key string, err error, fields ...zap.Field) {
		mu.Lock()
		var span *Span
		if queue := pending[key]; len(queue) != 0 {
			span = queue[0]
			pending[key] = queue[1:]
		}
		mu.Unlock()
		span.End(err, fields...)
	}
	hooks := &httptrace.ClientTrace{
		GetConn: func(string) { begin("connection", "connection_acquire") },
		GotConn: func(info httptrace.GotConnInfo) {
			mu.Lock()
			firstByteSeen = false
			mu.Unlock()
			end("connection", nil, zap.Bool("connection_reused", info.Reused), zap.Bool("connection_was_idle", info.WasIdle))
			begin("write", "request_write")
		},
		DNSStart:          func(httptrace.DNSStartInfo) { begin("dns", "dns") },
		DNSDone:           func(info httptrace.DNSDoneInfo) { end("dns", info.Err) },
		ConnectStart:      func(network, address string) { begin("connect:"+network+address, "tcp_connect") },
		ConnectDone:       func(network, address string, err error) { end("connect:"+network+address, err) },
		TLSHandshakeStart: func() { begin("tls", "tls_handshake") },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { end("tls", err) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			end("write", info.Err)
			mu.Lock()
			if !finished && info.Err == nil {
				span := Start(ctx, "wait_first_response_byte")
				if firstByteSeen {
					// A server can respond before the upload completes (including
					// 100 Continue). There is no post-upload wait in that case.
					span.End(nil, zap.Bool("early_response", true))
				} else {
					pending["first_byte"] = append(pending["first_byte"], span)
				}
			}
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			firstByteSeen = true
			var span *Span
			if queue := pending["first_byte"]; len(queue) != 0 {
				span = queue[0]
				pending["first_byte"] = queue[1:]
			}
			mu.Unlock()
			span.End(nil)
		},
	}
	resp, err := do(req.WithContext(httptrace.WithClientTrace(ctx, hooks)))
	fields := []zap.Field{}
	if resp != nil {
		fields = append(fields, zap.Int("http_status", resp.StatusCode), zap.String("http_protocol", resp.Proto),
			zap.Int64("response_content_length", resp.ContentLength))
	}
	call.End(err, fields...)
	// An incomplete callback is not a measured successful network phase.
	mu.Lock()
	finished = true
	remaining := pending
	pending = make(map[string][]*Span)
	mu.Unlock()
	for _, queue := range remaining {
		for _, span := range queue {
			span.End(err, zap.Bool("phase_completed", false))
		}
	}
	if resp != nil && resp.Body != nil {
		resp.Body = &body{ReadCloser: resp.Body, span: Start(ctx, "response_body", fields...), started: time.Now()}
	}
	return resp, err
}

type body struct {
	io.ReadCloser
	span      *Span
	started   time.Time
	mu        sync.Mutex
	bytes     int64
	read      time.Duration
	closed    sync.Once
	closeErr  error
	active    int
	closing   bool
	endErr    error
	endReason string
	logged    bool
}

func (b *body) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.active++
	b.mu.Unlock()
	start := time.Now()
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	b.bytes += int64(n)
	b.read += time.Since(start)
	b.active--
	if err != nil {
		if err == io.EOF {
			if b.endErr == nil {
				b.endReason = "eof"
			}
		} else {
			b.endErr, b.endReason = err, "read_error"
		}
	}
	b.finishLocked()
	b.mu.Unlock()
	return n, err
}

// Called only after every already-started Read has accounted for its result.
func (b *body) finishLocked() {
	if b.logged || b.active != 0 || b.closing || b.endReason == "" {
		return
	}
	b.logged = true
	n, reading := b.bytes, b.read
	fields := []zap.Field{zap.Int64("body_bytes", n), zap.Float64("read_wait_ms", milliseconds(reading)), zap.String("body_end", b.endReason)}
	if elapsed := time.Since(b.started).Seconds(); elapsed > 0 {
		fields = append(fields, zap.Float64("body_bytes_per_second", float64(n)/elapsed))
	}
	b.span.End(b.endErr, fields...)
}

func (b *body) Close() error {
	b.closed.Do(func() {
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()
		// The underlying Close must be allowed to cancel an in-flight Read.
		b.closeErr = b.ReadCloser.Close()
		b.mu.Lock()
		b.closing = false
		if b.endReason == "" {
			b.endReason = "closed_before_eof"
		}
		if b.endErr == nil {
			b.endErr = b.closeErr
		}
		b.finishLocked()
		b.mu.Unlock()
	})
	return b.closeErr
}
