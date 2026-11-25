// Command bench runs a closed-loop synthetic HTTP posting workload.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/naraya07pedro-spec/ledgercore/internal/ledger"
)

type observation struct {
	duration time.Duration
	status   int
}

type locks struct {
	Samples     int `json:"samples"`
	WithWaiters int `json:"samples_with_lock_waiters"`
	PeakWaiters int `json:"peak_lock_waiters"`
}

func main() {
	n := flag.Int("n", 1000, "number of synthetic postings (1–20000)")
	workers := flag.Int("workers", 8, "closed-loop worker count (1–64)")
	base := flag.String("url", "http://127.0.0.1:18080", "ledger API base URL")
	flag.Parse()
	if err := run(*n, *workers, *base); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(n, workers int, base string) error {
	token := os.Getenv("LEDGER_OPERATOR_TOKEN")
	if n < 1 || n > 20000 || workers < 1 || workers > 64 || workers > n || !ledger.TokenValid(token) {
		return errors.New("supply a synthetic operator token and valid load parameters")
	}
	dsn := os.Getenv("ADMIN_DATABASE_URL")
	if dsn == "" {
		return errors.New("ADMIN_DATABASE_URL is required for lock sampling")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return errors.New("cannot connect to PostgreSQL sampler")
	}
	defer db.Close(context.Background())
	client := &http.Client{Timeout: 7 * time.Second, Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64}}
	defer client.CloseIdleConnections()
	runID := uuid.NewString()
	requests := make([][]byte, 2)
	for i, pair := range [][2]string{{ledger.DemoOperating, ledger.DemoReserve}, {ledger.DemoReserve, ledger.DemoOperating}} {
		requests[i], err = json.Marshal(ledger.Posting{Currency: "USD", Memo: "Synthetic load " + runID, Entries: []ledger.Entry{
			{Account: pair[0], Amount: -1}, {Account: pair[1], Amount: 1},
		}})
		if err != nil {
			return err
		}
	}
	sampling, stop := context.WithCancel(ctx)
	result := make(chan locks, 1)
	fail := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var stats locks
		for {
			select {
			case <-sampling.Done():
				result <- stats
				return
			case <-ticker.C:
				var count int
				err := db.QueryRow(sampling, `SELECT count(*) FROM pg_stat_activity
				 WHERE application_name='ledgercore' AND wait_event_type='Lock'`).Scan(&count)
				if err != nil {
					if sampling.Err() == nil {
						fail <- errors.New("PostgreSQL lock sampling failed")
						return
					}
					result <- stats
					return
				}
				stats.Samples++
				if count > 0 {
					stats.WithWaiters++
				}
				stats.PeakWaiters = max(stats.PeakWaiters, count)
			}
		}
	}()
	var wg sync.WaitGroup
	start := time.Now()
	observations := make([]observation, n)
	for worker := range workers {
		wg.Go(func() {
			for index := worker; index < n; index += workers {
				observations[index] = post(ctx, client, base, token, runID+"-"+strconv.Itoa(index), requests[index%2])
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	stop()
	var contention locks
	select {
	case err := <-fail:
		return err
	case contention = <-result:
	}
	counters := map[int]int{}
	latencies := make([]time.Duration, 0, n)
	for _, o := range observations {
		counters[o.status]++
		latencies = append(latencies, o.duration)
	}
	slices.Sort(latencies)
	percentile := func(q float64) float64 {
		return float64(latencies[int(math.Ceil(q*float64(n)))-1]) / float64(time.Millisecond)
	}
	report := map[string]any{
		"run_id": runID, "attempted": n, "workers": workers, "status_counts": counters, "seconds": elapsed.Seconds(),
		"attempted_per_second": float64(n) / elapsed.Seconds(), "committed_per_second": float64(counters[201]) / elapsed.Seconds(),
		"failure_rate": float64(n-counters[201]) / float64(n), "latency_ms": map[string]float64{"p50": percentile(.50), "p95": percentile(.95), "p99": percentile(.99)},
		"postgres_lock_sampling": contention,
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if err := out.Encode(report); err != nil {
		return err
	}
	if counters[201] != n {
		return errors.New("one or more load requests failed")
	}
	return nil
}

func post(ctx context.Context, client *http.Client, base, token, key string, body []byte) observation {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/journals", bytes.NewReader(body))
	if err != nil {
		return observation{time.Since(start), 0}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return observation{time.Since(start), 0}
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return observation{time.Since(start), 0}
	}
	return observation{time.Since(start), resp.StatusCode}
}
