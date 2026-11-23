package lifecycletest

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// dumpLimit bounds the goroutine dump a failure message carries.
	dumpLimit = 4 << 10
	// baselineStable is how many reads of the goroutine count, in a row, must agree for the
	// count to be taken as settled.
	baselineStable = 5
	// baselineLimit bounds the time TakeBaseline spends waiting for the count to settle.
	baselineLimit = 500 * time.Millisecond
)

// GoroutineBaseline is the goroutines that exist at a moment, to check later that a lane left
// none behind. A manager that is closed must leave no goroutine of its own: it is the owner of
// every goroutine of its sessions, and a plugin that leaks one per session leaks without end.
type GoroutineBaseline struct {
	count int
	known map[int]struct{} // ids of the goroutines that exist at the baseline
}

// TakeBaseline records the goroutines that exist now, once the count has settled: after a
// garbage collection, and once reading it repeatedly gives the same number, so that goroutines
// that are on their way out are not in it.
func TakeBaseline() GoroutineBaseline {
	runtime.GC()
	count := runtime.NumGoroutine()
	deadline := time.Now().Add(baselineLimit)
	for stable := 0; stable < baselineStable && time.Now().Before(deadline); {
		runtime.Gosched()
		pause(time.Millisecond)
		if n := runtime.NumGoroutine(); n == count {
			stable++
		} else {
			count, stable = n, 0
		}
	}
	known := make(map[int]struct{}, count)
	for _, g := range goroutineStacks() {
		known[g.id] = struct{}{}
	}
	return GoroutineBaseline{count: count, known: known}
}

// Check returns nil once the number of goroutines is back at the baseline, or lower, and gives
// the goroutines up to settle to get there, polling with growing pauses: a goroutine that has
// been told to return is on its way out for a moment. It returns an error when they do not,
// naming how many are left and listing, grouped by state, top frame and creator and bounded to
// 4 KiB, the goroutines that did not exist at the baseline.
func (b GoroutineBaseline) Check(settle time.Duration) error {
	runtime.GC()
	deadline := time.Now().Add(settle)
	wait := 20 * time.Microsecond
	for {
		n := runtime.NumGoroutine()
		if n <= b.count {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lifecycletest: %d goroutines %v after the lane was closed, %d at the baseline; the goroutines that were not there before, by state, top frame and creator:\n%s",
				n, settle, b.count, b.leaked())
		}
		runtime.Gosched()
		pause(wait)
		wait = min(2*wait, 10*time.Millisecond)
	}
}

// leaked describes the goroutines that did not exist at the baseline, grouped.
func (b GoroutineBaseline) leaked() string {
	var fresh []goroutineStack
	for _, g := range goroutineStacks() {
		if _, was := b.known[g.id]; !was {
			fresh = append(fresh, g)
		}
	}
	return groupStacks(fresh, dumpLimit)
}

// pause waits for d.
func pause(d time.Duration) {
	timer := time.NewTimer(d)
	<-timer.C
}

// goroutineStack is the part of one goroutine's stack that tells what it is.
type goroutineStack struct {
	id      int
	state   string // why it is not running: select, chan receive, sync.Mutex.Lock
	top     string // the function at the top of its stack
	creator string // the function that started it
}

// goroutineStacks returns every goroutine but the caller, which is the first one the runtime
// lists.
func goroutineStacks() []goroutineStack {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var stacks []goroutineStack
	for i, block := range strings.Split(string(buf), "\n\n") {
		if i == 0 {
			continue // the calling goroutine
		}
		if g, ok := parseStack(block); ok {
			stacks = append(stacks, g)
		}
	}
	return stacks
}

// parseStack reads one goroutine's block of a runtime stack dump:
//
//	goroutine 7 [select, 2 minutes]:
//	main.worker(0xc000012345)
//		/path/file.go:12 +0x34
//	created by main.start in goroutine 1
//		/path/file.go:8 +0x56
func parseStack(block string) (goroutineStack, bool) {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	head, ok := strings.CutPrefix(lines[0], "goroutine ")
	if !ok {
		return goroutineStack{}, false
	}
	idText, rest, ok := strings.Cut(head, " [")
	if !ok {
		return goroutineStack{}, false
	}
	id, err := strconv.Atoi(idText)
	if err != nil {
		return goroutineStack{}, false
	}
	state, _, _ := strings.Cut(strings.TrimSuffix(rest, "]:"), ",")
	g := goroutineStack{id: id, state: state}
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "\t"):
		case strings.HasPrefix(line, "created by "):
			creator, _, _ := strings.Cut(strings.TrimPrefix(line, "created by "), " in goroutine ")
			g.creator = creator
		case g.top == "":
			g.top = funcName(line)
		}
	}
	return g, true
}

// funcName cuts the arguments off a frame line: pkg.(*T).method(0xc0000, ...) is
// pkg.(*T).method.
func funcName(frame string) string {
	if i := strings.LastIndexByte(frame, '('); i > 0 {
		return frame[:i]
	}
	return frame
}

// groupStacks describes goroutines as one line for each state, top frame and creator, the
// most numerous first, cut to limit bytes.
func groupStacks(stacks []goroutineStack, limit int) string {
	counts := make(map[string]int)
	for _, g := range stacks {
		line := fmt.Sprintf("[%s] %s", g.state, g.top)
		if g.creator != "" {
			line += " <- " + g.creator
		}
		counts[line]++
	}
	lines := make([]string, 0, len(counts))
	for line := range counts {
		lines = append(lines, line)
	}
	slices.SortFunc(lines, func(a, b string) int {
		if byCount := counts[b] - counts[a]; byCount != 0 {
			return byCount
		}
		return strings.Compare(a, b)
	})
	var out strings.Builder
	for _, line := range lines {
		entry := fmt.Sprintf("%6d x %s\n", counts[line], line)
		if out.Len()+len(entry) > limit {
			out.WriteString("... (cut)\n")
			break
		}
		out.WriteString(entry)
	}
	return out.String()
}
