//go:build nexuscorelib

package main

// Self-diagnosis of the core's memory in real use, without content:
//   - keeps the soft memory limit above the live heap (adaptive);
//   - when the heap is large, at most once an hour, writes a heap profile
//     (stacks and sizes only) to the logs folder, keeping the last three,
//     and logs the top allocation sites as "heapsite:" lines.

import (
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"sort"
	"strings"
	"time"
)

const (
	memoryWatchEvery    = 30 * time.Second
	heapProfileAbove    = 150 << 20
	heapProfileEvery    = time.Hour
	heapProfilesKept    = 3
	heapSitesLogged     = 15
	heapSiteFrames      = 3
	memoryLimitHeadroom = 1.5
)

// adaptiveMemoryLimit is the soft limit for a live heap of live bytes.
func adaptiveMemoryLimit(live uint64) int64 {
	limit := int64(float64(live) * memoryLimitHeadroom)
	if limit < minCoreMemoryLimit {
		limit = minCoreMemoryLimit
	}
	return limit
}

func runMemoryWatch(logsDir string) {
	var lastProfile time.Time
	current := int64(minCoreMemoryLimit)
	samples := []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/memory/classes/heap/objects:bytes"}}
	t := time.NewTicker(memoryWatchEvery)
	defer t.Stop()
	for range t.C {
		metrics.Read(samples)
		live, alloc := samples[0].Value.Uint64(), samples[1].Value.Uint64()
		if !memoryLimitFromEnv() {
			if want := adaptiveMemoryLimit(live); math.Abs(float64(want-current)) > float64(current)/10 {
				debug.SetMemoryLimit(want)
				log.Printf("mem: soft limit %dMiB (live heap %dMiB)", want>>20, live>>20)
				current = want
			}
		}
		if alloc > heapProfileAbove && time.Since(lastProfile) >= heapProfileEvery {
			lastProfile = time.Now()
			writeHeapDiagnostics(logsDir, lastProfile)
		}
	}
}

// writeHeapDiagnostics writes heap-<time>.pprof and logs the top live
// allocation sites. A heap profile holds call stacks and sizes, no data.
func writeHeapDiagnostics(logsDir string, at time.Time) {
	if logsDir != "" {
		name := filepath.Join(logsDir, "heap-"+at.UTC().Format("20060102-150405")+".pprof")
		if f, err := os.Create(name); err == nil {
			_ = pprof.Lookup("heap").WriteTo(f, 0)
			_ = f.Close()
			pruneHeapProfiles(logsDir, heapProfilesKept)
		}
	}
	for _, s := range topHeapSites(heapSitesLogged, heapSiteFrames) {
		log.Printf("heapsite: %.1f %s", float64(s.bytes)/(1<<20), s.chain)
	}
}

func pruneHeapProfiles(dir string, keep int) {
	matches, _ := filepath.Glob(filepath.Join(dir, "heap-*.pprof"))
	sort.Strings(matches) // timestamped names sort by time
	for len(matches) > keep {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
}

type heapSite struct {
	chain string
	bytes int64
}

// topHeapSites aggregates live (in-use) heap by the first frames outside
// the runtime, scaled for sampling as pprof does.
func topHeapSites(n, frames int) []heapSite {
	var records []runtime.MemProfileRecord
	count, _ := runtime.MemProfile(nil, false)
	for {
		records = make([]runtime.MemProfileRecord, count+50)
		got, ok := runtime.MemProfile(records, false)
		if ok {
			records = records[:got]
			break
		}
		count = got
	}
	rate := float64(runtime.MemProfileRate)
	bySite := map[string]int64{}
	for _, r := range records {
		inuse, objs := r.InUseBytes(), r.InUseObjects()
		if inuse <= 0 || objs <= 0 {
			continue
		}
		scaled := float64(inuse)
		if rate > 1 {
			avg := float64(inuse) / float64(objs)
			scaled = float64(inuse) / (1 - math.Exp(-avg/rate))
		}
		bySite[siteChain(r.Stack(), frames)] += int64(scaled)
	}
	out := make([]heapSite, 0, len(bySite))
	for chain, b := range bySite {
		out = append(out, heapSite{chain: chain, bytes: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].bytes > out[j].bytes })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func siteChain(stack []uintptr, frames int) string {
	it := runtime.CallersFrames(stack)
	var names []string
	for {
		f, more := it.Next()
		fn := f.Function
		if fn != "" && !strings.HasPrefix(fn, "runtime.") {
			if i := strings.LastIndex(fn, "/"); i >= 0 {
				fn = fn[i+1:]
			}
			names = append(names, fn)
			if len(names) == frames {
				break
			}
		}
		if !more {
			break
		}
	}
	if len(names) == 0 {
		return "(runtime)"
	}
	return strings.Join(names, " < ")
}
