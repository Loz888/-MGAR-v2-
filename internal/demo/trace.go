package demo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/observability"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Collect an actual local OTLP export, retaining the audit-projection label.
// This does not pretend reconstructed zero-duration events are timing spans.
func collectTrace(ctx context.Context, a observability.Audit) (json.RawMessage, string, error) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, "missing", e
	}
	type capture struct {
		wire []byte
		ids  []string
		err  error
	}
	received := make(chan capture, 1)
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		var message collector.ExportTraceServiceRequest
		if r.Method != "POST" || r.URL.Path != "/v1/traces" || e != nil || proto.Unmarshal(raw, &message) != nil {
			http.Error(w, "invalid_otlp", 400)
			return
		}
		ids := []string{}
		for _, rs := range message.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					ids = append(ids, hex.EncodeToString(s.SpanId))
				}
			}
		}
		wire, e := protojson.Marshal(&message)
		select {
		case received <- capture{wire, ids, e}:
		default:
			http.Error(w, "duplicate", 409)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	if _, e = observability.ExportLocal(ctx, a, "http://"+listener.Addr().String()); e != nil {
		return nil, "missing", e
	}
	select {
	case r := <-received:
		return r.wire, observability.Assess(a, r.ids), r.err
	default:
		return nil, "missing", errors.New("trace_not_received")
	}
}
