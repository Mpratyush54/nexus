//go:build race

package daemon

// raceEnabled: the race detector instruments allocations, so absolute
// allocation limits in tests only hold without it.
const raceEnabled = true
