// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §fuzz).
//
//	fuzz -runs 10000 -intent=true            property run (INTENT geometry = ADR-0010 candidate)
//	fuzz -runs 10000 -intent=false           same, NAIVE relative geometry (shows wrong-intent volume)
//	fuzz -mut nocycle|nocascade|unstablesort|dropack   seeded bugs: the fuzzer must catch them
//	fuzz -replay file.json                   re-run a shrunk regression fixture
//	fuzz -selftest                           fractional-index properties
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

func genScript(seed int64, intent bool) *Script {
	r := rand.New(rand.NewSource(seed))
	sc := &Script{Seed: seed, Clients: 3 + r.Intn(3), Intent: intent}
	n := 60 + r.Intn(160)
	horizon := n/4 + 5 // dense: many events per tick => real concurrency
	for i := 0; i < n; i++ {
		k := kEdit
		switch x := r.Intn(100); {
		case x < 4:
			k = kOffline
		case x < 10:
			k = kOnline
		case x < 12:
			k = kRevoke
		case x < 14:
			k = kGrant
		case x < 17:
			k = kRetransmit
		}
		sc.Events = append(sc.Events, Event{T: r.Intn(horizon), C: r.Intn(sc.Clients), Kind: k, A: r.Uint32(), B: r.Uint32(), D: r.Uint32()})
	}
	sort.SliceStable(sc.Events, func(i, j int) bool { return sc.Events[i].T < sc.Events[j].T })
	return sc
}

func fails(sc *Script) bool { return len(Run(sc).Failures) > 0 }

// shrink: delta debugging over the event list.
func shrink(sc *Script) *Script {
	cur := *sc
	cur.Events = append([]Event{}, sc.Events...)
	for chunk := len(cur.Events) / 2; chunk >= 1; chunk /= 2 {
		for i := 0; i+chunk <= len(cur.Events); {
			cand := cur
			cand.Events = append(append([]Event{}, cur.Events[:i]...), cur.Events[i+chunk:]...)
			if fails(&cand) {
				cur = cand
			} else {
				i += chunk
			}
		}
	}
	return &cur
}

func selftest() int {
	r := rand.New(rand.NewSource(7))
	bad := 0
	keys := []string{""}
	for i := 0; i < 200000; i++ {
		// pick random adjacent pair from a sorted list; insert between; list stays sorted & unique
		sort.Strings(keys)
		j := r.Intn(len(keys) + 1)
		a, b := "", ""
		if j > 0 {
			a = keys[j-1]
		}
		if j < len(keys) {
			b = keys[j]
		}
		if a == b && a != "" {
			continue
		}
		if b != "" && a >= b {
			continue
		}
		k := Between(a, b)
		if !ValidKey(k) || (a != "" && k <= a) || (b != "" && k >= b) {
			fmt.Printf("BAD Between(%q,%q)=%q\n", a, b, k)
			bad++
			if bad > 5 {
				return 1
			}
		}
		keys = append(keys, k)
		if len(keys) > 300 {
			keys = keys[100:]
		}
	}
	// pathological: always insert at the front / back / same gap -> key length growth
	for _, mode := range []string{"front", "back", "gap"} {
		a, b, maxLen := "", "", 0
		prev := "m"
		for i := 0; i < 2000; i++ {
			var k string
			switch mode {
			case "front":
				k = Between("", prev)
				prev = k
			case "back":
				k = Between(prev, "")
				prev = k
			case "gap":
				if i == 0 {
					a, b = "a", "b"
				}
				k = Between(a, b)
				b = k
			}
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}
		fmt.Printf("fractional index worst case %-5s: 2000 inserts -> max key length %d\n", mode, maxLen)
	}
	fmt.Println("selftest ok")
	return 0
}

func main() {
	runs := flag.Int("runs", 10000, "number of random scripts")
	seed := flag.Int64("seed", 1, "base seed")
	intent := flag.Bool("intent", true, "INTENT geometry (true) or NAIVE relative geometry (false)")
	mut := flag.String("mut", "", "seeded bug: nocycle|nocascade|unstablesort|dropack")
	replay := flag.String("replay", "", "replay a script json")
	st := flag.Bool("selftest", false, "fractional index selftest")
	out := flag.String("out", "", "write summary json here")
	reg := flag.String("regress", "", "directory for shrunk failing scripts")
	flag.Parse()

	switch *mut {
	case "nocycle":
		Mut.NoCycleCheck = true
	case "nocascade":
		Mut.NoCascade = true
	case "unstablesort":
		Mut.UnstableSort = true
	case "dropack":
		Mut.DropAckSome = true
	case "silentdrop":
		Mut.SilentDrop = true
	case "noauthz":
		Mut.NoAuthz = true
	case "":
	default:
		fmt.Println("unknown mutation")
		os.Exit(2)
	}
	if *st {
		os.Exit(selftest())
	}
	if *replay != "" {
		b, _ := os.ReadFile(*replay)
		var sc Script
		_ = json.Unmarshal(b, &sc)
		res := Run(&sc)
		fmt.Printf("failures=%d %v\n", len(res.Failures), res.Failures)
		if len(os.Args) > 0 && os.Getenv("DEBUG_LOG") != "" {
			dumpDebug(&sc)
		}
		return
	}

	type agg struct {
		failed   int
		firstBad []*Script
		failKind map[string]int
		metrics  map[string]int
		ops      int
		det      int
	}
	A := agg{failKind: map[string]int{}, metrics: map[string]int{}}
	var mu sync.Mutex
	jobs := make(chan int64, 64)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sd := range jobs {
				sc := genScript(sd, *intent)
				res := Run(sc)
				// determinism: the same script must give the same state and the same log
				again := Run(sc)
				mu.Lock()
				if again.Final != res.Final || again.LogHash != res.LogHash {
					A.det++
				}
				for k, v := range res.Metrics {
					A.metrics[k] += v
				}
				A.ops += res.Metrics["generated"]
				if len(res.Failures) > 0 {
					A.failed++
					kind := res.Failures[0]
					if len(kind) > 40 {
						kind = kind[:40]
					}
					A.failKind[kind]++
					if len(A.firstBad) < 3 {
						A.firstBad = append(A.firstBad, sc)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < *runs; i++ {
		jobs <- *seed + int64(i)
	}
	close(jobs)
	wg.Wait()

	summary := map[string]any{
		"arch": runtime.GOARCH, "os": runtime.GOOS, "go": runtime.Version(), "intentMode": *intent, "mutation": *mut,
		"runs": *runs, "baseSeed": *seed, "scriptsFailed": A.failed, "nondeterministicRuns": A.det, "opsGenerated": A.ops,
		"failureKinds": A.failKind, "metrics": A.metrics, "seconds": time.Since(start).Seconds(),
	}
	if len(A.firstBad) > 0 {
		var shrunk []map[string]any
		for i, sc := range A.firstBad {
			s2 := shrink(sc)
			res := Run(s2)
			shrunk = append(shrunk, map[string]any{"seed": sc.Seed, "eventsBefore": len(sc.Events), "eventsAfter": len(s2.Events), "failures": res.Failures})
			if *reg != "" {
				_ = os.MkdirAll(*reg, 0o755)
				b, _ := json.MarshalIndent(s2, "", " ")
				name := fmt.Sprintf("fail-%s-%d-%d.json", map[bool]string{true: "intent", false: "naive"}[*intent], sc.Seed, i)
				if *mut != "" {
					name = fmt.Sprintf("mut-%s-%d.json", *mut, sc.Seed)
				}
				_ = os.WriteFile(filepath.Join(*reg, name), b, 0o644)
			}
		}
		summary["shrunk"] = shrunk
	}
	b, _ := json.MarshalIndent(summary, "", " ")
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, b, 0o644)
	}
	if A.failed > 0 || A.det > 0 {
		os.Exit(1)
	}
}

// dumpDebug prints the sequenced log and the canonical diff between fold(log) and fold(log+log).
func dumpDebug(sc *Script) {
	s := newSim(sc)
	_ = s
	res := Run(sc)
	_ = res
	r := runForLog(sc)
	for _, o := range r {
		fmt.Printf("seq=%d %-11s actor=%d id=%s page=%s cell=%s par=%q key=%q key2=%q val=%q xy=%d,%d rel=%v\n", o.Seq, o.T, o.Actor, o.ID, o.Page, o.Cell, o.Par, o.Key, o.Term, o.Val, o.X, o.Y, o.Rel)
	}
	a := fold(r, nil).Canon()
	b := fold(append(append([]*Op{}, r...), r...), nil).Canon()
	fmt.Println("---- once:\n" + a + "---- twice:\n" + b)
}
