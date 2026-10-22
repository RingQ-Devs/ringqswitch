package web

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostHealth samples the server's CPU, memory and disk use for the
// dashboard's System Health card. The last few minutes are kept for the
// small trend lines.
type hostHealth struct {
	disk string // a path on the disk to report (the recordings folder)

	mu                  sync.Mutex
	prevIdle, prevTotal uint64
	cur                 healthInfo
	cpuHist, memHist    []float64
}

type healthInfo struct {
	CPUPercent  float64   `json:"cpu_pct"`
	CPUs        int       `json:"cpus"`
	Load1       float64   `json:"load1"`
	MemTotalMB  float64   `json:"mem_total_mb"`
	MemFreeMB   float64   `json:"mem_free_mb"`
	MemPercent  float64   `json:"mem_pct"`
	DiskTotalGB float64   `json:"disk_total_gb"`
	DiskFreeGB  float64   `json:"disk_free_gb"`
	DiskPercent float64   `json:"disk_pct"`
	CPUHistory  []float64 `json:"cpu_history"`
	MemHistory  []float64 `json:"mem_history"`
	Available   bool      `json:"available"` // false where /proc is missing
}

const (
	healthEvery   = 5 * time.Second
	healthHistory = 36 // 3 minutes
)

func (h *hostHealth) run(stop <-chan struct{}) {
	h.sample()
	t := time.NewTicker(healthEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.sample()
		}
	}
}

func (h *hostHealth) sample() {
	info := healthInfo{CPUs: runtime.NumCPU()}
	idle, total, cpuOK := readCPU()
	if total, avail, ok := readMem(); ok {
		info.Available = true
		info.MemTotalMB = round1(float64(total) / 1024)
		info.MemFreeMB = round1(float64(avail) / 1024)
		if total > 0 {
			info.MemPercent = round1(100 * float64(total-avail) / float64(total))
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			info.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if total, free, ok := diskUsage(h.disk); ok && total > 0 {
		info.DiskTotalGB = round1(float64(total) / (1 << 30))
		info.DiskFreeGB = round1(float64(free) / (1 << 30))
		info.DiskPercent = round1(100 * float64(total-free) / float64(total))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if cpuOK {
		if h.prevTotal > 0 && total > h.prevTotal {
			busy := float64((total - h.prevTotal) - (idle - h.prevIdle))
			info.CPUPercent = round1(math.Max(0, math.Min(100, 100*busy/float64(total-h.prevTotal))))
		} else {
			info.CPUPercent = h.cur.CPUPercent
		}
		h.prevIdle, h.prevTotal = idle, total
	}
	h.cpuHist = appendHist(h.cpuHist, info.CPUPercent)
	h.memHist = appendHist(h.memHist, info.MemPercent)
	h.cur = info
}

func (h *hostHealth) snapshot() healthInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.cur
	out.CPUHistory = append([]float64(nil), h.cpuHist...)
	out.MemHistory = append([]float64(nil), h.memHist...)
	return out
}

func appendHist(list []float64, v float64) []float64 {
	list = append(list, v)
	if len(list) > healthHistory {
		list = list[len(list)-healthHistory:]
	}
	return list
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// readCPU returns the idle and total jiffies of all CPUs (Linux).
func readCPU() (idle, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	for i, s := range f[1:] {
		n, _ := strconv.ParseUint(s, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return idle, total, true
}

// readMem returns the total and available memory in KiB (Linux).
func readMem() (total, avail uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	return total, avail, total > 0
}
