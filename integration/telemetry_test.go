package integration

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/naraya07pedro-spec/ledgercore/internal/httpapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestTraceExportCorrelatesWithHTTPAndLogs(t *testing.T) {
	f := newFixture(t, 10)
	exported := make(chan *collector.ExportTraceServiceRequest, 8)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		var payload collector.ExportTraceServiceRequest
		if err != nil || r.URL.Path != "/v1/traces" || proto.Unmarshal(raw, &payload) != nil {
			http.Error(w, "invalid trace", 400)
			return
		}
		exported <- &payload
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	exporter, err := otlptracehttp.New(f.ctx, otlptracehttp.WithEndpointURL(receiver.URL+"/v1/traces"))
	must(t, err)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer func() {
		otel.SetTracerProvider(previous)
		must(t, provider.Shutdown(f.ctx))
	}()
	var logs capturedLogs
	api := httptest.NewServer(httpapi.New(f.store, slog.New(slog.NewJSONHandler(&logs, nil))))
	defer api.Close()
	body, err := json.Marshal(move(source, destination, 2))
	must(t, err)
	response, raw := apiRequest(t, api.Client(), api.URL, "POST", "/v1/journals", f.operator, "trace-me", body)
	if response.StatusCode != 201 {
		t.Fatalf("posting failed: %s", raw)
	}
	traceID := response.Header.Get("X-Trace-ID")
	if len(traceID) != 32 || !strings.Contains(logs.String(), traceID) {
		t.Fatal("response and log trace IDs differ")
	}
	seen := map[string]bool{}
	for range 3 {
		select {
		case payload := <-exported:
			for _, resource := range payload.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					for _, span := range scope.Spans {
						if hex.EncodeToString(span.TraceId) != traceID {
							t.Fatal("exported trace ID differs")
						}
						seen[span.Name] = true
					}
				}
			}
		case <-time.After(time.Second):
			t.Fatal("expected synchronous OTLP trace export")
		}
	}
	for _, name := range []string{"POST /v1/journals", "journal.write", "journal.postgres_post"} {
		if !seen[name] {
			t.Fatalf("missing exported span %s", name)
		}
	}
}
