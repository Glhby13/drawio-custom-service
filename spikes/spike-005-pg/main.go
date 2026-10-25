// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §PostgreSQL).
// Measures a PostgreSQL append-only operation log with a per-document sequencer: single-op transactions, group commit
// (batching), open-loop target rates (100 / 1,000 / 10,000 ops/s), replay, snapshot insert, compaction, growth.
// Run against a DISPOSABLE database only (it DROPs and recreates its own tables).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var payload = strings.Repeat("x", 190) // ~270 B per op with envelope fields, like the encoded semantic op in spike-002

type item struct {
	doc int64
	at  time.Time
	res chan time.Duration
}

func pct(d []time.Duration, q float64) float64 {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return float64(d[int(float64(len(d)-1)*q)].Microseconds()) / 1000
}

func setup(ctx context.Context, p *pgxpool.Pool, docs int) {
	for _, q := range []string{
		`DROP TABLE IF EXISTS ops, documents, snapshots`,
		`CREATE TABLE documents (id bigint PRIMARY KEY, head_seq bigint NOT NULL DEFAULT 0)`,
		`CREATE TABLE ops (document_id bigint NOT NULL, server_seq bigint NOT NULL, operation_id text NOT NULL, actor text NOT NULL,
		   type text NOT NULL, payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
		   PRIMARY KEY (document_id, server_seq))`,
		`CREATE UNIQUE INDEX ops_operation_id ON ops (document_id, operation_id)`,
		`CREATE TABLE snapshots (document_id bigint NOT NULL, server_seq bigint NOT NULL, data bytea NOT NULL, PRIMARY KEY (document_id, server_seq))`,
	} {
		if _, err := p.Exec(ctx, q); err != nil {
			panic(err)
		}
	}
	_, err := p.Exec(ctx, `INSERT INTO documents SELECT g, 0 FROM generate_series(1,$1) g`, docs)
	if err != nil {
		panic(err)
	}
}

// commitBatch is the sequencer transaction: reserve n sequence numbers on the document row, insert n operations.
func commitBatch(ctx context.Context, p *pgxpool.Pool, doc int64, n int, tag string) error {
	return pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		var head int64
		if err := tx.QueryRow(ctx, `UPDATE documents SET head_seq = head_seq + $2 WHERE id = $1 RETURNING head_seq`, doc, n).Scan(&head); err != nil {
			return err
		}
		ids := make([]string, n)
		seqs := make([]int64, n)
		for i := 0; i < n; i++ {
			seqs[i] = head - int64(n) + 1 + int64(i)
			ids[i] = fmt.Sprintf("%s-%d-%d", tag, doc, seqs[i])
		}
		_, err := tx.Exec(ctx, `INSERT INTO ops (document_id, server_seq, operation_id, actor, type, payload)
		   SELECT $1, s, i, 'u-1', 'setGeometry', jsonb_build_object('cell','c1','x',s,'pad',$4::text)
		   FROM unnest($2::bigint[], $3::text[]) AS t(s, i)`, doc, seqs, ids, payload)
		return err
	})
}

type runOut struct {
	TargetOpsPerSec    int     `json:"targetOpsPerSec"`
	Docs               int     `json:"docs"`
	BatchWindowMs      float64 `json:"batchWindowMs"`
	AchievedOpsSec     float64 `json:"achievedOpsPerSec"`
	Ops                int     `json:"ops"`
	P50ms              float64 `json:"p50ms"`
	P95ms              float64 `json:"p95ms"`
	P99ms              float64 `json:"p99ms"`
	MaxMs              float64 `json:"maxMs"`
	AvgBatch           float64 `json:"avgBatch"`
	Errors             int64   `json:"errors"`
	BacklogAtWindowEnd int     `json:"backlogAtWindowEnd"`
	Saturated          bool    `json:"saturated"`
}

// openLoop: arrivals at a fixed rate, independent of completion; latency = enqueue -> commit.
func openLoop(ctx context.Context, p *pgxpool.Pool, rate, docs int, window time.Duration, dur time.Duration) runOut {
	setup(ctx, p, docs)
	queues := make([]chan item, docs)
	var wg sync.WaitGroup
	var errs, batches, batched atomic.Int64
	var mu sync.Mutex
	var lats []time.Duration
	var doneAts []time.Time
	stop := make(chan struct{})
	for d := 0; d < docs; d++ {
		queues[d] = make(chan item, 1<<16)
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			for {
				var first item
				select {
				case first = <-queues[d]:
				case <-stop:
					return
				}
				batch := []item{first}
				if window > 0 {
					timer := time.NewTimer(window)
				collect:
					for len(batch) < 512 {
						select {
						case it := <-queues[d]:
							batch = append(batch, it)
						case <-timer.C:
							break collect
						}
					}
					timer.Stop()
				} else {
					// no batching: one transaction per op (the naive design)
				}
				if window == 0 {
					for _, it := range batch {
						err := commitBatch(ctx, p, it.doc, 1, "a")
						if err != nil {
							errs.Add(1)
						}
						now := time.Since(it.at)
						mu.Lock()
						lats = append(lats, now)
						doneAts = append(doneAts, time.Now())
						mu.Unlock()
						batches.Add(1)
						batched.Add(1)
					}
					continue
				}
				err := commitBatch(ctx, p, first.doc, len(batch), "b")
				if err != nil {
					errs.Add(1)
				}
				now := time.Now()
				mu.Lock()
				for _, it := range batch {
					lats = append(lats, now.Sub(it.at))
					doneAts = append(doneAts, now)
				}
				mu.Unlock()
				batches.Add(1)
				batched.Add(int64(len(batch)))
			}
		}(d)
	}
	start := time.Now()
	n := 0
	interval := time.Second / time.Duration(rate)
	if rate > 2000 { // generate in 1 ms ticks to avoid timer resolution limits
		perTick := rate / 1000
		tk := time.NewTicker(time.Millisecond)
		for time.Since(start) < dur {
			<-tk.C
			for i := 0; i < perTick; i++ {
				queues[n%docs] <- item{doc: int64(n%docs) + 1, at: time.Now()}
				n++
			}
		}
		tk.Stop()
	} else {
		next := start
		for time.Since(start) < dur {
			next = next.Add(interval)
			if w := time.Until(next); w > 0 {
				time.Sleep(w)
			}
			queues[n%docs] <- item{doc: int64(n%docs) + 1, at: time.Now()}
			n++
		}
	}
	// drain
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pending := 0
		for _, q := range queues {
			pending += len(q)
		}
		mu.Lock()
		done := len(lats)
		mu.Unlock()
		if pending == 0 && done >= n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	inWindow := 0
	for _, t := range doneAts {
		if t.Sub(start) <= dur {
			inWindow++
		}
	}
	avg := 0.0
	if batches.Load() > 0 {
		avg = float64(batched.Load()) / float64(batches.Load())
	}
	return runOut{TargetOpsPerSec: rate, Docs: docs, BatchWindowMs: float64(window.Microseconds()) / 1000, AchievedOpsSec: float64(inWindow) / dur.Seconds(), Ops: len(lats), BacklogAtWindowEnd: n - inWindow, Saturated: float64(inWindow) < 0.95*float64(n),
		P50ms: pct(lats, 0.5), P95ms: pct(lats, 0.95), P99ms: pct(lats, 0.99), MaxMs: pct(lats, 1), AvgBatch: avg, Errors: errs.Load()}
}

func main() {
	dsn := flag.String("dsn", os.Getenv("PG_DSN"), "postgres DSN of a DISPOSABLE database")
	dur := flag.Duration("dur", 10*time.Second, "duration per open-loop run")
	quick := flag.Bool("quick", false, "short smoke run")
	out := flag.String("out", "", "write json here")
	flag.Parse()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		panic(err)
	}
	cfg.MaxConns = 32
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer p.Close()
	res := map[string]any{"arch": runtime.GOARCH, "go": runtime.Version(), "ranAt": time.Now().Format(time.RFC3339)}
	var ver, sc, wsm, fsync, shb string
	_ = p.QueryRow(ctx, `SHOW server_version`).Scan(&ver)
	_ = p.QueryRow(ctx, `SHOW synchronous_commit`).Scan(&sc)
	_ = p.QueryRow(ctx, `SHOW wal_sync_method`).Scan(&wsm)
	_ = p.QueryRow(ctx, `SHOW fsync`).Scan(&fsync)
	_ = p.QueryRow(ctx, `SHOW shared_buffers`).Scan(&shb)
	res["postgres"] = map[string]string{"version": ver, "synchronous_commit": sc, "wal_sync_method": wsm, "fsync": fsync, "shared_buffers": shb}

	type cfgRun struct {
		rate, docs int
		win        time.Duration
	}
	var runs []cfgRun
	rates := []int{100, 1000, 10000}
	if *quick {
		rates = []int{100, 1000}
	}
	for _, r := range rates {
		for _, docs := range []int{1, 200} {
			for _, w := range []time.Duration{0, 2 * time.Millisecond} {
				runs = append(runs, cfgRun{r, docs, w})
			}
		}
	}
	// warm-up: first WAL segments, connection pool, plan cache
	_ = openLoop(ctx, p, 500, 50, 2*time.Millisecond, 4*time.Second)
	var open []runOut
	for _, c := range runs {
		_, _ = p.Exec(ctx, `CHECKPOINT`)
		d := *dur
		if *quick {
			d = 3 * time.Second
		}
		// 10,000 ops/s without batching on one doc cannot be sustained by design; cap the run so the report shows saturation
		if c.win == 0 && c.rate >= 10000 {
			d = 4 * time.Second
		}
		o := openLoop(ctx, p, c.rate, c.docs, c.win, d)
		open = append(open, o)
		fmt.Fprintf(os.Stderr, "rate=%d docs=%d win=%.0fms -> achieved=%.0f/s backlog=%d saturated=%v p50=%.2f p95=%.2f p99=%.2f avgBatch=%.1f err=%d\n", o.TargetOpsPerSec, o.Docs, o.BatchWindowMs, o.AchievedOpsSec, o.BacklogAtWindowEnd, o.Saturated, o.P50ms, o.P95ms, o.P99ms, o.AvgBatch, o.Errors)
	}
	res["openLoop"] = open

	// replay / snapshot / compaction on one document
	setup(ctx, p, 1)
	n := 1_000_000
	if *quick {
		n = 100_000
	}
	t0 := time.Now()
	_, err = p.Exec(ctx, `INSERT INTO ops (document_id, server_seq, operation_id, actor, type, payload)
	   SELECT 1, g, 'seed-'||g, 'u-1', 'setGeometry', jsonb_build_object('cell','c'||(g%5000),'x',g,'pad',$2::text) FROM generate_series(1,$1) g`, n, payload)
	if err != nil {
		panic(err)
	}
	bulkMs := time.Since(t0).Milliseconds()
	_, _ = p.Exec(ctx, `UPDATE documents SET head_seq=$1 WHERE id=1`, n)
	_, _ = p.Exec(ctx, `ANALYZE ops`)
	var size int64
	_ = p.QueryRow(ctx, `SELECT pg_total_relation_size('ops')`).Scan(&size)
	replay := []map[string]any{}
	for _, k := range []int{1000, 10000, 100000, n} {
		if k > n {
			continue
		}
		t := time.Now()
		rows, err := p.Query(ctx, `SELECT server_seq, type, payload FROM ops WHERE document_id=1 AND server_seq > $1 ORDER BY server_seq`, n-k)
		if err != nil {
			panic(err)
		}
		cnt := 0
		for rows.Next() {
			var s int64
			var ty string
			var raw []byte
			if err := rows.Scan(&s, &ty, &raw); err != nil {
				panic(err)
			}
			cnt++
		}
		rows.Close()
		replay = append(replay, map[string]any{"ops": cnt, "fetchMs": time.Since(t).Milliseconds()})
	}
	// snapshot insert (gzip XML for ~500 cells ~ 4 KB, 5,000 cells ~ 37 KB; use 40 KB)
	blob := make([]byte, 40*1024)
	var snapLat []time.Duration
	for i := 0; i < 200; i++ {
		t := time.Now()
		_, err := p.Exec(ctx, `INSERT INTO snapshots VALUES (1, $1, $2)`, int64(i+1), blob)
		if err != nil {
			panic(err)
		}
		snapLat = append(snapLat, time.Since(t))
	}
	// range read for reconnect gap
	gap := []map[string]any{}
	for _, g := range []int{10, 1000, 100000} {
		var lat []time.Duration
		for i := 0; i < 20; i++ {
			t := time.Now()
			rows, _ := p.Query(ctx, `SELECT server_seq, payload FROM ops WHERE document_id=1 AND server_seq > $1 ORDER BY server_seq LIMIT $2`, n/2+i, g)
			for rows.Next() {
			}
			rows.Close()
			lat = append(lat, time.Since(t))
		}
		gap = append(gap, map[string]any{"gapOps": g, "p50ms": pct(lat, 0.5), "p95ms": pct(lat, 0.95)})
	}
	t1 := time.Now()
	tag, _ := p.Exec(ctx, `DELETE FROM ops WHERE document_id=1 AND server_seq <= $1`, n/2)
	compactMs := time.Since(t1).Milliseconds()
	t2 := time.Now()
	_, _ = p.Exec(ctx, `VACUUM (ANALYZE) ops`)
	vacuumMs := time.Since(t2).Milliseconds()
	var after int64
	_ = p.QueryRow(ctx, `SELECT pg_total_relation_size('ops')`).Scan(&after)
	res["bulkAndReplay"] = map[string]any{"seedOps": n, "bulkInsertMs": bulkMs, "tableBytes": size, "bytesPerOp": size / int64(n), "replay": replay,
		"snapshotInsert40KB": map[string]any{"p50ms": pct(snapLat, 0.5), "p95ms": pct(snapLat, 0.95)}, "reconnectGapRead": gap,
		"compactDeleteHalf": map[string]any{"rows": tag.RowsAffected(), "ms": compactMs, "vacuumMs": vacuumMs, "bytesAfterVacuum": after}}
	b, _ := json.MarshalIndent(res, "", " ")
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, b, 0o644)
	}
}
