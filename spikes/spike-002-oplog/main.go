// BUKAN KODE PRODUKSI - disposable spike (SPIKE-001 §op log). Measures only the CPU side of a
// server-sequenced operation log: sequencing + JSON encoding, LWW apply, replay from snapshot+log,
// snapshot cost, and encoded size per op. Database insert latency is NOT measured here (no
// PostgreSQL available for the spike); see docs/spikes/SPIKE-001 for that methodology.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"
)

type Op struct {
	OperationID    string          `json:"operationId"`
	DocumentID     string          `json:"documentId"`
	ActorID        string          `json:"actorId"`
	ActorType      string          `json:"actorType"`
	ClientSequence int64           `json:"clientSequence"`
	ServerSequence int64           `json:"serverSequence"`
	HLC            int64           `json:"hlc"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
}

type reg struct {
	val string
	seq int64
}

type cell struct {
	props   map[string]reg
	deleted int64 // tombstone seq, 0 = alive
}

type doc struct{ cells map[string]*cell }

func newDoc() *doc { return &doc{cells: map[string]*cell{}} }

func (d *doc) clone() *doc {
	n := &doc{cells: make(map[string]*cell, len(d.cells))}
	for id, c := range d.cells {
		p := make(map[string]reg, len(c.props))
		for k, v := range c.props {
			p[k] = v
		}
		n.cells[id] = &cell{props: p, deleted: c.deleted}
	}
	return n
}

type payload struct {
	CellID string `json:"cellId"`
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
}

// apply: per-property LWW by server sequence; add-wins insert; delete = tombstone.
func (d *doc) apply(o *Op, p *payload) {
	c := d.cells[p.CellID]
	switch o.Type {
	case "addCell":
		if c == nil {
			d.cells[p.CellID] = &cell{props: map[string]reg{}}
		}
		return
	case "deleteCell":
		if c != nil {
			c.deleted = o.ServerSequence
		}
		return
	}
	if c == nil || c.deleted != 0 {
		return
	}
	if r, ok := c.props[p.Key]; !ok || r.seq < o.ServerSequence {
		c.props[p.Key] = reg{val: p.Value, seq: o.ServerSequence}
	}
}

var types = []string{"setGeometry", "setGeometry", "setGeometry", "setValue", "setStyle", "setStyle", "addCell", "deleteCell"}

func genOps(n, cells int, rng *rand.Rand) []Op {
	ops := make([]Op, n)
	for i := range ops {
		t := types[rng.Intn(len(types))]
		p := payload{CellID: fmt.Sprintf("c%d", rng.Intn(cells))}
		switch t {
		case "setGeometry":
			p.Key, p.Value = "geometry", fmt.Sprintf(`{"x":%d,"y":%d,"width":120,"height":60}`, rng.Intn(4000), rng.Intn(4000))
		case "setValue":
			p.Key, p.Value = "value", fmt.Sprintf("Label %d", rng.Intn(1e6))
		case "setStyle":
			p.Key, p.Value = "style.fillColor", fmt.Sprintf("#%06x", rng.Intn(0xffffff))
		}
		b, _ := json.Marshal(p)
		ops[i] = Op{OperationID: fmt.Sprintf("018f6c1e-6a1b-7c3d-9e4f-%012d", i), DocumentID: "d-1", ActorID: "u-1", ActorType: "human",
			ClientSequence: int64(i), Type: t, Payload: b}
	}
	return ops
}

func pct(d []time.Duration, q float64) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*q)]
}

func main() {
	rng := rand.New(rand.NewSource(1))
	out := map[string]any{"go": runtime.Version(), "cpus": runtime.NumCPU()}

	// 1) sequencing + encode + LWW apply for bursts sized like 1s at 100 / 1k / 10k ops/sec
	seqRes := []map[string]any{}
	for _, rate := range []int{100, 1000, 10000} {
		ops := genOps(rate, 500, rng)
		d := newDoc()
		var seq int64
		var mu sync.Mutex
		lat := make([]time.Duration, len(ops))
		bytes := 0
		start := time.Now()
		for i := range ops {
			t0 := time.Now()
			mu.Lock()
			seq++
			ops[i].ServerSequence, ops[i].HLC = seq, time.Now().UnixNano()
			var p payload
			_ = json.Unmarshal(ops[i].Payload, &p)
			d.apply(&ops[i], &p)
			b, _ := json.Marshal(&ops[i])
			mu.Unlock()
			bytes += len(b)
			lat[i] = time.Since(t0)
		}
		el := time.Since(start)
		seqRes = append(seqRes, map[string]any{"opsInBurst": rate, "totalMs": float64(el.Microseconds()) / 1000,
			"p50us": pct(lat, 0.5).Microseconds(), "p99us": pct(lat, 0.99).Microseconds(), "avgEncodedBytes": bytes / len(ops),
			"cpuShareOfOneCorePct": 100 * el.Seconds()})
	}
	out["sequenceEncodeApply"] = seqRes

	// 2) replay: snapshot + K ops, 5,000-cell document
	replay := []map[string]any{}
	base := newDoc()
	for i := 0; i < 5000; i++ {
		base.cells[fmt.Sprintf("c%d", i)] = &cell{props: map[string]reg{"geometry": {val: `{"x":0}`}, "value": {val: "v"}}}
	}
	for _, k := range []int{1000, 10000, 100000, 1000000} {
		ops := genOps(k, 5000, rng)
		ps := make([]payload, k)
		for i := range ops {
			ops[i].ServerSequence = int64(i + 1)
			_ = json.Unmarshal(ops[i].Payload, &ps[i])
		}
		t0 := time.Now()
		d := base.clone()
		cl := time.Since(t0)
		for i := range ops {
			d.apply(&ops[i], &ps[i])
		}
		replay = append(replay, map[string]any{"ops": k, "snapshotCloneMs": float64(cl.Microseconds()) / 1000, "replayMs": float64(time.Since(t0).Microseconds()) / 1000})
	}
	out["replayFromSnapshot"] = replay

	// 3) concurrent documents: 200 docs x 1,000 ops, one sequencer (mutex) per document
	t0 := time.Now()
	var wg sync.WaitGroup
	for dn := 0; dn < 200; dn++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			ops := genOps(1000, 500, r)
			d := newDoc()
			var mu sync.Mutex
			for i := range ops {
				mu.Lock()
				ops[i].ServerSequence = int64(i + 1)
				var p payload
				_ = json.Unmarshal(ops[i].Payload, &p)
				d.apply(&ops[i], &p)
				_, _ = json.Marshal(&ops[i])
				mu.Unlock()
			}
		}(int64(dn))
	}
	wg.Wait()
	el := time.Since(t0)
	out["concurrentDocs"] = map[string]any{"documents": 200, "opsPerDoc": 1000, "totalMs": el.Milliseconds(), "opsPerSec": int(200000 / el.Seconds())}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
