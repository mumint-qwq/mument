// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gtsteffaniak/go-logger/logger"
)

// previewStats is deliberately cumulative and lock-free. It is read by tests
// and by one low-frequency logger; it is not a per-request metrics pipeline.
type previewStats struct {
	cacheHits       atomic.Uint64
	cacheMisses     atomic.Uint64
	cacheCorrupt    atomic.Uint64
	sharedWaits     atomic.Uint64
	canceledWaiters atomic.Uint64
	canceledQueued  atomic.Uint64
	fullDecodes     atomic.Uint64
	originalServed  atomic.Uint64
	embeddedPreview atomic.Uint64
	subprocesses    atomic.Uint64
	overBudget      atomic.Uint64
	headerRejected  atomic.Uint64
	sourceChanged   atomic.Uint64
	storeFailures   atomic.Uint64
	generatedBytes  atomic.Uint64
	panics          atomic.Uint64
	tasksStarted    atomic.Uint64
	tasksCompleted  atomic.Uint64
	tasksFailed     atomic.Uint64
	memoryReclaims  atomic.Uint64
	entryRejected   atomic.Uint64
	waiterRejected  atomic.Uint64
	headerReads     atomic.Uint64

	activeTasks       atomic.Int64
	waitingTasks      atomic.Int64
	httpWaiters       atomic.Int64
	peakActive        atomic.Int64
	peakWaiting       atomic.Int64
	peakReservedBytes atomic.Int64
	reservedBytes     atomic.Int64
	taskSlotsInUse    atomic.Int64
	peakTaskSlots     atomic.Int64

	queueWaitNanos atomic.Uint64
	generateNanos  atomic.Uint64
	publishNanos   atomic.Uint64
}

// PreviewStats is a point-in-time diagnostic snapshot. Memory fields are
// reported separately on purpose: Go heap, process RSS and cgroup counters
// answer different questions and must not be collapsed into one value.
type PreviewStats struct {
	CacheHits       uint64 `json:"cacheHits"`
	CacheMisses     uint64 `json:"cacheMisses"`
	CacheCorrupt    uint64 `json:"cacheCorrupt"`
	SharedWaits     uint64 `json:"sharedWaits"`
	CanceledWaiters uint64 `json:"canceledWaiters"`
	CanceledQueued  uint64 `json:"canceledQueued"`
	FullDecodes     uint64 `json:"fullDecodes"`
	OriginalServed  uint64 `json:"originalServed"`
	EmbeddedPreview uint64 `json:"embeddedPreview"`
	Subprocesses    uint64 `json:"subprocesses"`
	OverBudget      uint64 `json:"overBudget"`
	HeaderRejected  uint64 `json:"headerRejected"`
	SourceChanged   uint64 `json:"sourceChanged"`
	StoreFailures   uint64 `json:"storeFailures"`
	GeneratedBytes  uint64 `json:"generatedBytes"`
	Panics          uint64 `json:"panics"`
	TasksStarted    uint64 `json:"tasksStarted"`
	TasksCompleted  uint64 `json:"tasksCompleted"`
	TasksFailed     uint64 `json:"tasksFailed"`
	MemoryReclaims  uint64 `json:"memoryReclaims"`
	EntryRejected   uint64 `json:"entryRejected"`
	WaiterRejected  uint64 `json:"waiterRejected"`
	HeaderReads     uint64 `json:"headerReads"`
	ActiveTasks     int64  `json:"activeTasks"`
	WaitingTasks    int64  `json:"waitingTasks"`
	HTTPWaiters     int64  `json:"httpWaiters"`
	PeakActive      int64  `json:"peakActive"`
	PeakWaiting     int64  `json:"peakWaiting"`
	PeakReserved    int64  `json:"peakReservedBytes"`
	ReservedBytes   int64  `json:"reservedBytes"`
	TaskSlots       int64  `json:"taskSlots"`
	TaskSlotsInUse  int64  `json:"taskSlotsInUse"`
	PeakTaskSlots   int64  `json:"peakTaskSlots"`
	QueueWaitNanos  uint64 `json:"queueWaitNanos"`
	GenerateNanos   uint64 `json:"generateNanos"`
	PublishNanos    uint64 `json:"publishNanos"`

	BudgetBytes   int64  `json:"budgetBytes"`
	MaxConcurrent int    `json:"maxConcurrent"`
	GoHeapAlloc   uint64 `json:"goHeapAllocBytes"`
	GoHeapSys     uint64 `json:"goHeapSysBytes"`
	GoHeapIdle    uint64 `json:"goHeapIdleBytes"`
	GoNumGC       uint32 `json:"goNumGC"`
	Goroutines    int    `json:"goroutines"`
	ProcessRSS    int64  `json:"processRSSBytes"`
	CgroupCurrent int64  `json:"cgroupMemoryCurrentBytes"`
	CgroupPeak    int64  `json:"cgroupMemoryPeakBytes"`
	CgroupMax     int64  `json:"cgroupMemoryMaxBytes"`
	CgroupOOM     int64  `json:"cgroupOOMEvents"`
	CgroupOOMKill int64  `json:"cgroupOOMKillEvents"`
}

func newPreviewStats() *previewStats {
	return &previewStats{}
}

func (s *previewStats) snapshot(budgetBytes int64, maxConcurrent, taskSlots int) PreviewStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	cg := readCgroupMemory()
	return PreviewStats{
		CacheHits:       s.cacheHits.Load(),
		CacheMisses:     s.cacheMisses.Load(),
		CacheCorrupt:    s.cacheCorrupt.Load(),
		SharedWaits:     s.sharedWaits.Load(),
		CanceledWaiters: s.canceledWaiters.Load(),
		CanceledQueued:  s.canceledQueued.Load(),
		FullDecodes:     s.fullDecodes.Load(),
		OriginalServed:  s.originalServed.Load(),
		EmbeddedPreview: s.embeddedPreview.Load(),
		Subprocesses:    s.subprocesses.Load(),
		OverBudget:      s.overBudget.Load(),
		HeaderRejected:  s.headerRejected.Load(),
		SourceChanged:   s.sourceChanged.Load(),
		StoreFailures:   s.storeFailures.Load(),
		GeneratedBytes:  s.generatedBytes.Load(),
		Panics:          s.panics.Load(),
		TasksStarted:    s.tasksStarted.Load(),
		TasksCompleted:  s.tasksCompleted.Load(),
		TasksFailed:     s.tasksFailed.Load(),
		MemoryReclaims:  s.memoryReclaims.Load(),
		EntryRejected:   s.entryRejected.Load(),
		WaiterRejected:  s.waiterRejected.Load(),
		HeaderReads:     s.headerReads.Load(),
		ActiveTasks:     s.activeTasks.Load(),
		WaitingTasks:    s.waitingTasks.Load(),
		HTTPWaiters:     s.httpWaiters.Load(),
		PeakActive:      s.peakActive.Load(),
		PeakWaiting:     s.peakWaiting.Load(),
		PeakReserved:    s.peakReservedBytes.Load(),
		ReservedBytes:   s.reservedBytes.Load(),
		TaskSlots:       int64(taskSlots),
		TaskSlotsInUse:  s.taskSlotsInUse.Load(),
		PeakTaskSlots:   s.peakTaskSlots.Load(),
		QueueWaitNanos:  s.queueWaitNanos.Load(),
		GenerateNanos:   s.generateNanos.Load(),
		PublishNanos:    s.publishNanos.Load(),
		BudgetBytes:     budgetBytes,
		MaxConcurrent:   maxConcurrent,
		GoHeapAlloc:     m.HeapAlloc,
		GoHeapSys:       m.HeapSys,
		GoHeapIdle:      m.HeapIdle,
		GoNumGC:         m.NumGC,
		Goroutines:      runtime.NumGoroutine(),
		ProcessRSS:      cg.rss,
		CgroupCurrent:   cg.current,
		CgroupPeak:      cg.peak,
		CgroupMax:       cg.max,
		CgroupOOM:       cg.oom,
		CgroupOOMKill:   cg.oomKill,
	}
}

func (s *Service) Stats() PreviewStats {
	if s == nil {
		return PreviewStats{}
	}
	return s.stats.snapshot(s.budget.maxBytes, s.budget.maxConcurrent, s.tasks.capacity())
}

// sampleLoop is the only background goroutine A adds. It is tied to the service
// context and emits one aggregate line every 30 seconds, but only when at least
// one counter changed since the previous line. It never reads smaps or writes
// profiles, and it is not a per-request metrics pipeline.
func (s *Service) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var lastTotal uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot := s.Stats()
			total := snapshot.CacheHits + snapshot.CacheMisses + snapshot.TasksStarted +
				snapshot.OverBudget + snapshot.HeaderRejected + snapshot.SourceChanged +
				snapshot.StoreFailures + snapshot.EntryRejected + snapshot.WaiterRejected +
				snapshot.CanceledWaiters + snapshot.CanceledQueued + snapshot.Panics
			if total == lastTotal {
				continue
			}
			lastTotal = total
			logger.Infof("preview stats: hit=%d miss=%d corrupt=%d shared=%d decode=%d original=%d embedded=%d subprocess=%d over_budget=%d header_rejected=%d source_changed=%d store_failures=%d panics=%d entry_rejected=%d waiter_rejected=%d canceled_waiters=%d canceled_queued=%d started=%d completed=%d failed=%d active=%d waiting=%d http_waiters=%d task_slots=%d task_slots_in_use=%d peak_active=%d peak_waiting=%d peak_reserved_mib=%d reserved_mib=%d queue_wait_ms=%d generate_ms=%d publish_ms=%d heap_mib=%d heap_sys_mib=%d heap_idle_mib=%d goroutines=%d rss_mib=%d cgroup_current_mib=%d cgroup_peak_mib=%d cgroup_max_mib=%d oom=%d oom_kill=%d",
				snapshot.CacheHits, snapshot.CacheMisses, snapshot.CacheCorrupt, snapshot.SharedWaits,
				snapshot.FullDecodes, snapshot.OriginalServed, snapshot.EmbeddedPreview, snapshot.Subprocesses,
				snapshot.OverBudget, snapshot.HeaderRejected, snapshot.SourceChanged, snapshot.StoreFailures,
				snapshot.Panics, snapshot.EntryRejected, snapshot.WaiterRejected, snapshot.CanceledWaiters,
				snapshot.CanceledQueued, snapshot.TasksStarted, snapshot.TasksCompleted, snapshot.TasksFailed,
				snapshot.ActiveTasks, snapshot.WaitingTasks, snapshot.HTTPWaiters, snapshot.TaskSlots,
				snapshot.TaskSlotsInUse, snapshot.PeakActive, snapshot.PeakWaiting,
				snapshot.PeakReserved>>20, snapshot.ReservedBytes>>20,
				snapshot.QueueWaitNanos/1_000_000, snapshot.GenerateNanos/1_000_000, snapshot.PublishNanos/1_000_000,
				snapshot.GoHeapAlloc>>20, snapshot.GoHeapSys>>20, snapshot.GoHeapIdle>>20,
				snapshot.Goroutines, snapshot.ProcessRSS>>20, snapshot.CgroupCurrent>>20,
				snapshot.CgroupPeak>>20, snapshot.CgroupMax>>20, snapshot.CgroupOOM, snapshot.CgroupOOMKill)
		}
	}
}

type cgroupMemory struct {
	rss     int64
	current int64
	peak    int64
	max     int64
	oom     int64
	oomKill int64
}

func readCgroupMemory() cgroupMemory {
	var cg cgroupMemory
	cg.rss = readProcStatusRSS()
	paths := []struct {
		dst  *int64
		file string
	}{
		{&cg.current, "/sys/fs/cgroup/memory.current"},
		{&cg.peak, "/sys/fs/cgroup/memory.peak"},
		{&cg.max, "/sys/fs/cgroup/memory.max"},
	}
	for _, p := range paths {
		if value, ok := readIntFile(p.file); ok {
			*p.dst = value
		}
	}
	if f, err := os.Open("/sys/fs/cgroup/memory.events"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 2 {
				continue
			}
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "oom":
				cg.oom = value
			case "oom_kill":
				cg.oomKill = value
			}
		}
	}
	if cg.current == 0 {
		// cgroup v1 fallback used by older hosts.
		if value, ok := readIntFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok {
			cg.current = value
		}
		if value, ok := readIntFile("/sys/fs/cgroup/memory/memory.max_usage_in_bytes"); ok {
			cg.peak = value
		}
	}
	if cg.max == 0 {
		if value, ok := readIntFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
			cg.max = value
		}
	}
	return cg
}

func readProcStatusRSS() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "VmRSS:") {
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

func readIntFile(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
