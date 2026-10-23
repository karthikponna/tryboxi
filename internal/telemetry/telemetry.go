// Package telemetry sets up tracing and shared metrics. Spans always go to
// an in-memory recorder (served at /v1/traces, shown by `sandboxctl trace`)
// and to Prometheus histograms; when an OTLP endpoint is configured they are
// also exported to any OpenTelemetry collector (Jaeger, Tempo, Honeycomb...).
package telemetry

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is the daemon's tracer.
func Tracer() trace.Tracer { return otel.Tracer("sandboxd") }

// Start begins a span with common attributes.
func Start(ctx context.Context, name string, kv ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(kv...))
}

// End records err (if any) and ends the span.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Sandbox and Exec attribute helpers.
func Sandbox(id string) attribute.KeyValue { return attribute.String("sandbox.id", id) }
func Exec(id string) attribute.KeyValue    { return attribute.String("exec.id", id) }

// Shared Prometheus collectors (registered by the API server).
var (
	SpanSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "sandboxd_span_seconds", Help: "Duration of traced phases (create.restore, exec, workspace.sync, ...).",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 120, 600},
	}, []string{"name", "status"})
	NetEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sandboxd_net_events_total", Help: "Egress decisions and secret audit events by type.",
	}, []string{"type"})
	NetBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sandboxd_net_bytes_total", Help: "Bytes relayed by the egress proxy.",
	}, []string{"direction"})
)

// SpanRecord is one finished span.
type SpanRecord struct {
	TraceID  string            `json:"trace_id"`
	SpanID   string            `json:"span_id"`
	ParentID string            `json:"parent_id,omitempty"`
	Name     string            `json:"name"`
	Start    time.Time         `json:"start"`
	DurMs    float64           `json:"dur_ms"`
	Status   string            `json:"status"`
	Error    string            `json:"error,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// Recorder keeps the most recent spans in memory.
type Recorder struct {
	mu    sync.Mutex
	spans []SpanRecord
	max   int
}

func (r *Recorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range spans {
		rec := SpanRecord{
			TraceID: s.SpanContext().TraceID().String(), SpanID: s.SpanContext().SpanID().String(),
			Name: s.Name(), Start: s.StartTime(), DurMs: float64(s.EndTime().Sub(s.StartTime()).Microseconds()) / 1000,
			Status: "ok",
		}
		if s.Parent().IsValid() {
			rec.ParentID = s.Parent().SpanID().String()
		}
		if s.Status().Code == codes.Error {
			rec.Status, rec.Error = "error", s.Status().Description
		}
		if attrs := s.Attributes(); len(attrs) > 0 {
			rec.Attrs = map[string]string{}
			for _, a := range attrs {
				rec.Attrs[string(a.Key)] = a.Value.Emit()
			}
		}
		SpanSeconds.WithLabelValues(rec.Name, rec.Status).Observe(rec.DurMs / 1000)
		r.spans = append(r.spans, rec)
	}
	if over := len(r.spans) - r.max; over > 0 {
		r.spans = append([]SpanRecord(nil), r.spans[over:]...)
	}
	return nil
}

func (r *Recorder) Shutdown(context.Context) error { return nil }

// Trace is a group of spans sharing a trace id.
type Trace struct {
	TraceID string       `json:"trace_id"`
	Root    string       `json:"root"`
	Start   time.Time    `json:"start"`
	DurMs   float64      `json:"dur_ms"`
	Spans   []SpanRecord `json:"spans"`
}

// Traces returns recent traces touching sandboxID/execID (both optional).
func (r *Recorder) Traces(sandboxID, execID string, limit int) []Trace {
	r.mu.Lock()
	spans := append([]SpanRecord(nil), r.spans...)
	r.mu.Unlock()
	match := map[string]bool{}
	for _, s := range spans {
		if (sandboxID == "" || s.Attrs["sandbox.id"] == sandboxID) && (execID == "" || s.Attrs["exec.id"] == execID) {
			match[s.TraceID] = true
		}
	}
	byTrace := map[string]*Trace{}
	var order []string
	for _, s := range spans {
		if !match[s.TraceID] {
			continue
		}
		t, ok := byTrace[s.TraceID]
		if !ok {
			t = &Trace{TraceID: s.TraceID, Start: s.Start}
			byTrace[s.TraceID] = t
			order = append(order, s.TraceID)
		}
		t.Spans = append(t.Spans, s)
		if s.Start.Before(t.Start) {
			t.Start = s.Start
		}
	}
	var out []Trace
	for _, id := range order {
		t := byTrace[id]
		sort.Slice(t.Spans, func(i, j int) bool { return t.Spans[i].Start.Before(t.Spans[j].Start) })
		end := t.Start
		for _, s := range t.Spans {
			if e := s.Start.Add(time.Duration(s.DurMs * float64(time.Millisecond))); e.After(end) {
				end = e
			}
			if s.ParentID == "" || t.Root == "" {
				if s.ParentID == "" {
					t.Root = s.Name
				}
			}
		}
		if t.Root == "" && len(t.Spans) > 0 {
			t.Root = t.Spans[0].Name
		}
		t.DurMs = float64(end.Sub(t.Start).Microseconds()) / 1000
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Config selects exporters.
type Config struct {
	OTLPEndpoint string // e.g. http://127.0.0.1:4318 (also OTEL_EXPORTER_OTLP_ENDPOINT)
	Version      string
}

// Init installs the global tracer provider. The returned func flushes it.
func Init(ctx context.Context, c Config) (*Recorder, func(context.Context) error, error) {
	rec := &Recorder{max: 5000}
	res, _ := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName("sandboxd"), semconv.ServiceVersion(c.Version)))
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(rec)),
	}
	endpoint := c.OTLPEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint != "" {
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint+"/v1/traces"))
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return rec, tp.Shutdown, nil
}
