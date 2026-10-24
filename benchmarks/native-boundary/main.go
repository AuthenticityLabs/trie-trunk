package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
)

type config struct {
	Mode                  string
	Bytes, Chunk, Streams int
	TLS                   bool
	HeapProfile           string
}
type sample struct {
	Config                config  `json:"config"`
	DurationMS            float64 `json:"ingest_ms"`
	MiBPerSecond          float64 `json:"mib_per_second"`
	RetainedGoPayload     uint64  `json:"retained_go_payload_bytes"`
	RetainedNativePayload uint64  `json:"retained_native_payload_bytes"`
	ExplicitBridgeCopies  uint64  `json:"explicit_go_to_native_copy_bytes"`
	GoLiveDelta           int64   `json:"go_live_heap_delta_bytes"`
	GoTotalAlloc          uint64  `json:"go_total_alloc_bytes"`
	GoObjectsDelta        int64   `json:"go_live_objects_delta"`
	GoGCs                 uint32  `json:"go_gc_cycles_during_ingest"`
	GoGCPauseNS           uint64  `json:"go_gc_pause_ns_during_ingest"`
	CgoCalls              int64   `json:"cgo_calls_during_ingest"`
	MaxRSSBytes           uint64  `json:"process_peak_rss_bytes"`
	Sum                   uint64  `json:"checksum"`
}
type retained struct {
	goBuffers            [][]byte
	native               nativeSession
	goBytes, copies, sum uint64
}

func (r *retained) close() { r.native.close(); r.goBuffers = nil }
func receive(body io.Reader, c config) (r *retained, err error) {
	r = &retained{native: newNative()}
	defer func() {
		if err != nil {
			r.close()
		}
	}()
	var scratch []byte
	if c.Mode == "copy" {
		scratch = make([]byte, c.Chunk)
	}
	for left := c.Bytes; left > 0; {
		n := min(left, c.Chunk)
		var b []byte
		switch c.Mode {
		case "go", "duplicate":
			b = make([]byte, n)
		case "copy":
			b = scratch[:n]
		case "direct":
			b = r.native.buffer(n)
		default:
			return r, fmt.Errorf("invalid mode")
		}
		if _, err = io.ReadFull(body, b); err != nil {
			return r, err
		}
		r.sum += checksum(b)
		if c.Mode == "go" || c.Mode == "duplicate" {
			r.goBuffers = append(r.goBuffers, b)
			r.goBytes += uint64(n)
		}
		if c.Mode == "copy" || c.Mode == "duplicate" {
			copy(r.native.buffer(n), b)
			r.copies += uint64(n)
		}
		left -= n
	}
	var tail [1]byte
	n, e := body.Read(tail[:])
	if n != 0 || !errors.Is(e, io.EOF) {
		return r, fmt.Errorf("unexpected body tail: %d %v", n, e)
	}
	return r, nil
}

// A streaming sender: no full request-sized Go fixture obscures heap ownership.
type patternReader struct{ remaining, at int }

func (r *patternReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := 0; i < n; i++ {
		p[i] = byte(r.at + i)
	}
	r.at += n
	r.remaining -= n
	return n, nil
}
func expected(n int) uint64 {
	whole := n / 256
	tail := uint64(n % 256)
	return uint64(whole)*32640 + tail*(tail-1)/2
}
func run(c config) (out sample, err error) {
	if c.Bytes <= 0 || c.Chunk <= 0 || c.Chunk > 16<<20 || c.Streams <= 0 {
		return out, fmt.Errorf("invalid size, chunk or streams")
	}
	switch c.Mode {
	case "go", "duplicate", "copy", "direct":
	default:
		return out, fmt.Errorf("invalid mode")
	}
	held := make(chan *retained, c.Streams)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/warm" {
			w.WriteHeader(204)
			return
		}
		r, e := receive(q.Body, c)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		held <- r
		w.WriteHeader(204)
	}))
	if c.TLS {
		server.StartTLS()
	} else {
		server.Start()
	}
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Minute
	// Warm one connection; multi-stream connection growth remains in measurements.
	resp, e := client.Get(server.URL + "/warm")
	if e != nil {
		return out, e
	}
	resp.Body.Close()
	runtime.GC()
	var before, ingested, live runtime.MemStats
	runtime.ReadMemStats(&before)
	calls := runtime.NumCgoCall()
	begin := time.Now()
	failures := make(chan error, c.Streams)
	var wg sync.WaitGroup
	for i := 0; i < c.Streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, e := http.NewRequest("POST", server.URL+"/ingest", &patternReader{remaining: c.Bytes})
			if e != nil {
				failures <- e
				return
			}
			req.ContentLength = int64(c.Bytes)
			response, e := client.Do(req)
			if e != nil {
				failures <- e
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 204 {
				b, _ := io.ReadAll(response.Body)
				failures <- fmt.Errorf("HTTP %d: %s", response.StatusCode, b)
				return
			}
			_, e = io.Copy(io.Discard, response.Body)
			if e != nil {
				failures <- e
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(begin)
	calls = runtime.NumCgoCall() - calls
	runtime.ReadMemStats(&ingested)
	close(held)
	var leases []*retained
	for r := range held {
		leases = append(leases, r)
	}
	defer func() {
		for _, r := range leases {
			r.close()
		}
	}()
	close(failures)
	for e := range failures {
		return out, e
	}
	out.Config = c
	out.DurationMS = float64(elapsed) / float64(time.Millisecond)
	out.MiBPerSecond = float64(c.Bytes) * float64(c.Streams) / (1 << 20) / elapsed.Seconds()
	for _, r := range leases {
		if r.sum != expected(c.Bytes) {
			return out, fmt.Errorf("checksum mismatch")
		}
		out.RetainedGoPayload += r.goBytes
		out.RetainedNativePayload += r.native.bytes()
		out.ExplicitBridgeCopies += r.copies
		out.Sum += r.sum
	}
	// Excluded from latency/ingest GC counts; measures live ownership, not garbage.
	runtime.GC()
	runtime.ReadMemStats(&live)
	out.GoLiveDelta = int64(live.HeapAlloc) - int64(before.HeapAlloc)
	out.GoObjectsDelta = int64(live.HeapObjects) - int64(before.HeapObjects)
	out.GoTotalAlloc = ingested.TotalAlloc - before.TotalAlloc
	out.GoGCs = ingested.NumGC - before.NumGC
	out.GoGCPauseNS = ingested.PauseTotalNs - before.PauseTotalNs
	out.CgoCalls = calls
	var usage syscall.Rusage
	if e = syscall.Getrusage(syscall.RUSAGE_SELF, &usage); e != nil {
		return out, e
	}
	out.MaxRSSBytes = uint64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		out.MaxRSSBytes *= 1024
	}
	if c.HeapProfile != "" {
		f, e := os.Create(c.HeapProfile)
		if e != nil {
			return out, e
		}
		e = pprof.WriteHeapProfile(f)
		f.Close()
		if e != nil {
			return out, e
		}
	}
	runtime.KeepAlive(leases)
	return out, nil
}
func main() {
	c := config{}
	flag.StringVar(&c.Mode, "mode", "direct", "go | duplicate | copy | direct")
	flag.IntVar(&c.Bytes, "bytes", 32<<20, "bytes retained per stream")
	flag.IntVar(&c.Chunk, "chunk", 64<<10, "read chunk capacity")
	flag.IntVar(&c.Streams, "streams", 1, "concurrent HTTP requests")
	flag.BoolVar(&c.TLS, "tls", false, "use Go HTTPS/TLS")
	flag.StringVar(&c.HeapProfile, "heap-profile", "", "optional live Go heap profile")
	flag.Parse()
	out, e := run(c)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if e = json.NewEncoder(os.Stdout).Encode(out); e != nil {
		panic(e)
	}
}
