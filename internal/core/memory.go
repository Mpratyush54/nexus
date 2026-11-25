package core

import "runtime/metrics"

// goMemory reports the Go runtime's memory for diagnostics.get (bytes):
// what the runtime holds from the OS in all, the heap's objects, and the
// heap memory already returned to the OS.
func goMemory() map[string]uint64 {
	s := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/heap/live:bytes"},
	}
	metrics.Read(s)
	v := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	return map[string]uint64{"go_total": v(0), "heap_alloc": v(1), "heap_released": v(2), "heap_live": v(3)}
}
