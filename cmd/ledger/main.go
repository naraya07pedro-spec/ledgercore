package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/naraya07pedro-spec/ledgercore/internal/httpapi"
	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
	"github.com/naraya07pedro-spec/ledgercore/migrations"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("ledger command failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if len(os.Args) != 2 {
		return errors.New("usage: ledger migrate|seed|serve")
	}
	mode := os.Args[1]
	if mode != "migrate" && mode != "seed" && mode != "serve" {
		return errors.New("unknown ledger command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if mode != "serve" {
		bounded, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		dsn := os.Getenv("ADMIN_DATABASE_URL")
		if dsn == "" {
			return errors.New("ADMIN_DATABASE_URL is required")
		}
		admin, err := pgx.Connect(bounded, dsn)
		if err != nil {
			return errors.New("cannot connect as migration owner")
		}
		defer admin.Close(context.Background())
		if mode == "migrate" {
			return migrations.Apply(bounded, admin)
		}
		pool, err := poolFor(bounded)
		if err != nil {
			return err
		}
		defer pool.Close()
		return ledger.Seed(bounded, admin, ledger.Store{Pool: pool}, os.Getenv("LEDGER_OPERATOR_TOKEN"), os.Getenv("LEDGER_VIEWER_TOKEN"))
	}
	pool, err := poolFor(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "ledgercore"))),
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return errors.New("cannot create OTLP exporter")
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(512)))
	}
	tp := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(tp)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			logger.Warn("trace shutdown failed")
		}
	}()
	addr := os.Getenv("LEDGER_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv := &http.Server{Addr: addr, Handler: httpapi.New(ledger.Store{Pool: pool}, logger),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 6 * time.Second, WriteTimeout: 8 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.ListenAndServe() }()
	logger.Info("ledger starting", "address", addr)
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return fmt.Errorf("HTTP shutdown: %w", err)
		}
	}
	return nil
}

func poolFor(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	config.MaxConns = 16
	config.ConnConfig.ConnectTimeout = 2 * time.Second
	params := config.ConnConfig.RuntimeParams
	params["application_name"] = "ledgercore"
	params["lock_timeout"] = "1500ms"
	params["statement_timeout"] = "3000ms"
	params["idle_in_transaction_session_timeout"] = "7000ms"
	return pgxpool.NewWithConfig(ctx, config)
}
