package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Server struct {
	store    ledger.Store
	logger   *slog.Logger
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	started  time.Time
	active   atomic.Int64
	total    atomic.Uint64
}

type response struct {
	http.ResponseWriter
	status int
}

func (r *response) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *response) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

type action func(http.ResponseWriter, *http.Request, ledger.Principal) error

func New(store ledger.Store, logger *slog.Logger) http.Handler {
	reg := prometheus.NewRegistry()
	s := &Server{store: store, logger: logger, started: time.Now(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ledger_http_requests_total", Help: "Completed HTTP requests."}, []string{"route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ledger_http_duration_seconds", Help: "HTTP handler duration."}, []string{"route"}),
	}
	reg.MustRegister(s.requests, s.duration, prometheus.NewGoCollector())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { reply(w, 200, map[string]bool{"alive": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var ready bool
		err := s.store.Pool.QueryRow(ctx, "SELECT to_regprocedure('public.post_ledger(uuid,uuid,text,text,bytea,text,uuid,text[],bigint[])') IS NOT NULL").Scan(&ready)
		if err != nil || !ready {
			reply(w, 503, map[string]bool{"ready": false})
			return
		}
		reply(w, 200, map[string]bool{"ready": true})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		families, err := reg.Gather()
		if err != nil {
			http.Error(w, "metrics unavailable", 500)
			return
		}
		format := expfmt.NewFormat(expfmt.TypeTextPlain)
		w.Header().Set("Content-Type", string(format))
		encoder := expfmt.NewEncoder(w, format)
		for _, family := range families {
			if err := encoder.Encode(family); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /v1/accounts", s.auth(false, s.accounts))
	mux.HandleFunc("GET /v1/journals", s.auth(false, s.journals))
	mux.HandleFunc("POST /v1/journals", s.auth(true, s.post))
	mux.HandleFunc("POST /v1/journals/{id}/reversal", s.auth(true, s.reverse))
	mux.HandleFunc("POST /v1/reconciliations", s.auth(true, s.reconcile))
	mux.HandleFunc("GET /v1/reconciliations", s.auth(false, s.reconciliations))
	mux.HandleFunc("GET /v1/operations", s.auth(false, s.operations))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			pattern = "unmatched"
		}
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("ledgercore/http").Start(ctx, pattern, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		start := time.Now()
		out := &response{ResponseWriter: w}
		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		// Keep probes available when request capacity is saturated.
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			active := s.active.Add(1)
			defer s.active.Add(-1)
			if active > 64 {
				reply(out, 503, map[string]string{"error": "request capacity reached; retry with the same key"})
			}
		}
		if out.status == 0 {
			mux.ServeHTTP(out, r.WithContext(ctx))
		}
		elapsed := time.Since(start)
		s.total.Add(1)
		s.requests.WithLabelValues(pattern, strconv.Itoa(out.status)).Inc()
		s.duration.WithLabelValues(pattern).Observe(elapsed.Seconds())
		s.logger.InfoContext(ctx, "http request", "route", pattern, "status", out.status,
			"duration_ms", elapsed.Milliseconds(), "trace_id", span.SpanContext().TraceID().String())
	})
}

func (s *Server) auth(write bool, next action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !ledger.TokenValid(token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			reply(w, 401, map[string]string{"error": "authentication required"})
			return
		}
		p, err := s.store.Authenticate(r.Context(), token)
		if errors.Is(err, ledger.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			reply(w, 401, map[string]string{"error": "authentication required"})
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if write && p.Role != "operator" {
			reply(w, 403, map[string]string{"error": "operator role required"})
			return
		}
		if err := next(w, r, p); err != nil {
			s.fail(w, r, err)
		}
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := 503, "database operation unavailable; retry writes with the same idempotency key"
	switch {
	case errors.Is(err, ledger.ErrInvalid):
		status, msg = 400, err.Error()
	case errors.Is(err, ledger.ErrNotFound):
		status, msg = 404, err.Error()
	case errors.Is(err, ledger.ErrConflict), errors.Is(err, ledger.ErrFunds), errors.Is(err, ledger.ErrRange):
		status, msg = 409, err.Error()
	case errors.Is(err, ledger.ErrBusy):
		msg = err.Error()
	default:
		s.logger.WarnContext(r.Context(), "database operation failed", "error_type", fmt.Sprintf("%T", err),
			"trace_id", trace.SpanContextFromContext(r.Context()).TraceID().String())
	}
	reply(w, status, map[string]string{"error": msg})
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	var through *int64
	if raw := r.URL.Query().Get("through"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return ledger.ErrInvalid
		}
		through = &n
	}
	result, err := s.store.Accounts(r.Context(), p.Workspace, through)
	if err == nil {
		reply(w, 200, result)
	}
	return err
}

func (s *Server) journals(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	q := r.URL.Query()
	search, currency := q.Get("q"), q.Get("currency")
	if len(search) > 200 || strings.ContainsFunc(search, unicode.IsControl) ||
		currency != "" && !ledger.Currency(currency) {
		return ledger.ErrInvalid
	}
	var before int64
	if raw := q.Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return ledger.ErrInvalid
		}
		before = n
	}
	result, err := s.store.Journals(r.Context(), p.Workspace, search, currency, before)
	if err == nil {
		reply(w, 200, result)
	}
	return err
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return ledger.ErrInvalid
	}
	var body ledger.Posting
	if err := decode(w, r, &body); err != nil {
		return err
	}
	j, replay, err := s.store.Post(r.Context(), p.Workspace, r.Header.Get("Idempotency-Key"), body)
	return posted(w, j, replay, err)
}

func (s *Server) reverse(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return ledger.ErrInvalid
	}
	j, replay, err := s.store.Reverse(r.Context(), p.Workspace, r.Header.Get("Idempotency-Key"), r.PathValue("id"))
	return posted(w, j, replay, err)
}

func posted(w http.ResponseWriter, j ledger.Journal, replay bool, err error) error {
	if err != nil {
		return err
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
		reply(w, 200, j)
	} else {
		reply(w, 201, j)
	}
	return nil
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	var statement ledger.Statement
	if err := decode(w, r, &statement); err != nil {
		return err
	}
	report, err := s.store.Reconcile(r.Context(), p.Workspace, statement)
	if err == nil {
		reply(w, 201, report)
	}
	return err
}

func (s *Server) reconciliations(w http.ResponseWriter, r *http.Request, p ledger.Principal) error {
	result, err := s.store.Reconciliations(r.Context(), p.Workspace)
	if err == nil {
		reply(w, 200, result)
	}
	return err
}

func (s *Server) operations(w http.ResponseWriter, _ *http.Request, p ledger.Principal) error {
	stat := s.store.Pool.Stat()
	reply(w, 200, map[string]any{"role": p.Role, "scope": "api_instance", "uptimeSeconds": int64(time.Since(s.started).Seconds()),
		"httpRequests": s.total.Load(), "databaseAcquired": stat.AcquiredConns(), "databasePoolLimit": stat.MaxConns()})
	return nil
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // A disconnected client can replay committed writes.
}
