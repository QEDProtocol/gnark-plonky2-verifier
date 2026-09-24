//go:build proverbench

// Offline, serial Groth16 benchmark. Inputs/keys are read-only; no Setup call.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cf/gnark-plonky2-verifier/worker"
	"github.com/consensys/gnark-crypto/ecc"
	"github.com/rs/zerolog"
	"github.com/zilong-dai/gnark/backend/groth16"
	"github.com/zilong-dai/gnark/backend/witness"
	"github.com/zilong-dai/gnark/constraint"
	"github.com/zilong-dai/gnark/frontend"
	"github.com/zilong-dai/gnark/logger"
)

type sample struct {
	ID       string `json:"id"`
	Scenario string `json:"scenario"`
}
type manifest struct {
	Samples []sample `json:"ffi_samples"`
}
type context struct {
	Sample   string `json:"sample"`
	Scenario string `json:"scenario"`
	Sequence int    `json:"sequence"`
	Warmup   bool   `json:"warmup"`
}
type recorder struct {
	mu  sync.Mutex
	enc *json.Encoder
	ctx context
}

func (r *recorder) set(c context) { r.mu.Lock(); r.ctx = c; r.mu.Unlock() }
func (r *recorder) emit(event string, fields map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	fields["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["pid"] = os.Getpid()
	fields["context"] = r.ctx
	if err := r.enc.Encode(fields); err != nil {
		panic("metrics_write_failed")
	}
}
func (r *recorder) snapshot(event string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	f := map[string]any{"heap_alloc": m.HeapAlloc, "heap_inuse": m.HeapInuse, "heap_idle": m.HeapIdle,
		"heap_released": m.HeapReleased, "heap_sys": m.HeapSys, "total_alloc": m.TotalAlloc,
		"heap_objects": m.HeapObjects, "num_gc": m.NumGC, "pause_total_ns": m.PauseTotalNs, "goroutines": runtime.NumGoroutine()}
	if b, e := os.ReadFile("/proc/self/status"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			v := strings.Fields(line)
			if len(v) < 2 {
				continue
			}
			key := map[string]string{"VmRSS:": "rss_bytes", "VmHWM:": "rss_hwm_bytes", "VmSwap:": "swap_bytes"}[v[0]]
			if key != "" {
				n, _ := strconv.ParseUint(v[1], 10, 64)
				f[key] = n * 1024
			}
		}
	}
	r.emit(event, f)
}

// Only numeric timing/size fields from known gnark events are retained.
func (r *recorder) Write(b []byte) (int, error) {
	var v map[string]any
	if json.Unmarshal(b, &v) == nil {
		phase := map[string]string{"constraint system solver done": "solver", "prover done": "prover_compute", "proverbench compute H done": "compute_h"}[fmt.Sprint(v["message"])]
		if phase != "" {
			f := map[string]any{"stage": phase}
			if n, ok := v["took"].(float64); ok {
				f["duration_ms"] = n
			}
			if n, ok := v["nbConstraints"].(float64); ok {
				f["constraints"] = n
			}
			r.emit("internal_stage", f)
		}
	}
	return len(b), nil
}
func stage(r *recorder, name string, fn func() error) error {
	t := time.Now()
	err := fn()
	r.emit("stage", map[string]any{"stage": name, "duration_ms": float64(time.Since(t).Nanoseconds()) / 1e6, "ok": err == nil})
	return err
}

type prepared struct {
	cs constraint.ConstraintSystem
	pk groth16.ProvingKey
	vk groth16.VerifyingKey
}

var groups = map[string]string{"bridge": "", "deposit": "deposit_append", "withdrawal": "withdrawal_claim"}

func selectSamples(m manifest, scenario, id string) ([]sample, error) {
	if scenario != "all" {
		if _, ok := groups[scenario]; !ok {
			return nil, errors.New("invalid_scenario")
		}
	}
	seen := map[string]bool{}
	var out []sample
	for _, s := range m.Samples {
		if s.ID == "" || s.ID == "." || s.ID == ".." || filepath.Base(s.ID) != s.ID || strings.ContainsAny(s.ID, "\\\x00") {
			return nil, errors.New("invalid_sample_id")
		}
		if _, ok := groups[s.Scenario]; !ok || seen[s.ID] {
			return nil, errors.New("invalid_manifest")
		}
		seen[s.ID] = true
		if (scenario == "all" || s.Scenario == scenario) && (id == "" || s.ID == id) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no_matching_samples")
	}
	return out, nil
}
func proveOne(root string, s sample, p prepared, b proverBackend, r *recorder) error {
	var in [3]string
	if err := stage(r, "input_read", func() error {
		for i, n := range []string{"common_circuit_data.json", "proof_with_public_inputs.json", "verifier_only_circuit_data.json"} {
			data, err := os.ReadFile(filepath.Join(root, "ffi", s.ID, n))
			if err != nil {
				return errors.New("input_read_failed")
			}
			in[i] = string(data)
		}
		return nil
	}); err != nil {
		return err
	}
	var assignment *worker.CRVerifierCircuit
	if err := stage(r, "input_prepare", func() error { assignment = worker.BenchmarkAssignment(in[0], in[1], in[2]); return nil }); err != nil {
		return err
	}
	var w witness.Witness
	if err := stage(r, "new_witness", func() error { var e error; w, e = frontend.NewWitness(assignment, ecc.BN254.ScalarField()); return e }); err != nil {
		return errors.New("witness_failed")
	}
	var proof groth16.Proof
	if err := stage(r, "groth16_prove", func() error { var e error; proof, e = b.Prove(p.cs, p.pk, w); return e }); err != nil {
		return errors.New("prove_failed")
	}
	pub, err := w.Public()
	if err != nil {
		return errors.New("public_witness_failed")
	}
	// Always use the existing CPU verifier and original VK. Never retry failure.
	if err := stage(r, "verify", func() error { return groth16.Verify(proof, p.vk, pub) }); err != nil {
		return errors.New("verification_failed")
	}
	if err := stage(r, "serialize_native", func() error {
		out, e := json.Marshal(&worker.G16ProofWithPublicInputs{Proof: proof, PublicInputs: pub})
		if e != nil {
			return e
		}
		r.emit("output", map[string]any{"proof_json_bytes": len(out), "verified": true})
		return nil
	}); err != nil {
		return errors.New("serialize_failed")
	}
	return nil
}
func run(r *recorder) error {
	root := flag.String("artifacts", "", "read-only captured artifacts")
	scenario := flag.String("scenario", "all", "all, bridge, deposit, withdrawal")
	id := flag.String("sample", "", "single sample ID")
	cycles := flag.Int("cycles", 1, "measured cycles over selected samples")
	warmup := flag.Int("warmup", 1, "warm-up cycles, excluded from summary")
	interval := flag.Duration("interval", 0, "idle gap between requests")
	period := flag.Duration("sample-interval", time.Second, "memory sampling; 0 disables")
	backend := flag.String("backend", "cpu", "cpu, cpu-msm, or icicle-msm (requires matching build)")
	device := flag.Int("device", 0, "CUDA device ordinal")
	chunk := flag.Int("msm-chunk-size", defaultMSMChunkSize, "maximum points per GPU MSM chunk")
	internalChunks := flag.Int("msm-internal-chunks", defaultMSMInternalChunks, "ICICLE internal pipeline chunks (1,2,4,8)")
	gpuH := flag.Bool("gpu-h", false, "experimental GPU computeH (icicle-msm only)")
	backendDir := flag.String("backend-dir", "", "ICICLE CUDA backend library directory")
	gc := flag.Bool("gc-between", false, "explicit experimental GC; default off")
	heap := flag.String("heap-profile", "", "optional private final heap profile")
	cpu := flag.String("cpu-profile", "", "optional private CPU profile")
	list := flag.Bool("list", false, "list selected sample metadata only")
	flag.Parse()
	if *root == "" || *cycles < 1 || *warmup < 0 || *interval < 0 || *period < 0 {
		return errors.New("invalid_arguments")
	}
	b, err := selectBackendWithOptions(*backend, backendOptions{Device: *device, ChunkSize: *chunk, InternalChunks: *internalChunks, BackendDir: *backendDir, Recorder: r, GPUH: *gpuH})
	if err != nil {
		return err
	}
	defer b.Close()
	data, err := os.ReadFile(filepath.Join(*root, "manifest.json"))
	if err != nil {
		return errors.New("manifest_read_failed")
	}
	var m manifest
	if json.Unmarshal(data, &m) != nil {
		return errors.New("invalid_manifest")
	}
	samples, err := selectSamples(m, *scenario, *id)
	if err != nil {
		return err
	}
	if *list {
		for _, s := range samples {
			r.emit("fixture", map[string]any{"sample": s.ID, "scenario": s.Scenario})
		}
		return nil
	}
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	zerolog.DurationFieldUnit = time.Millisecond
	zerolog.DurationFieldInteger = false
	logger.Set(zerolog.New(r))
	r.emit("config", map[string]any{"backend": b.Name(), "gpu_h": *gpuH, "device": *device, "msm_chunk_size": *chunk, "msm_internal_chunks": *internalChunks, "go_version": runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "cycles": *cycles, "warmup": *warmup, "samples": len(samples), "interval_ms": float64(*interval) / 1e6, "gc_between": *gc, "sample_interval_ms": float64(*period) / 1e6})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	if *period > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(*period)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					r.snapshot("memory")
				case <-stop:
					return
				}
			}
		}()
	}
	defer func() { close(stop); wg.Wait() }()
	r.snapshot("startup")
	keys := map[string]prepared{}
	for _, s := range samples {
		if _, ok := keys[s.Scenario]; ok {
			continue
		}
		r.set(context{Scenario: s.Scenario})
		path := filepath.Join(*root, "keystore", groups[s.Scenario])
		var p prepared
		for _, item := range []struct {
			name string
			fn   func() error
		}{
			{"load_circuit", func() error {
				var e error
				p.cs, e = worker.ReadCircuit(ecc.BN254, filepath.Join(path, worker.CIRCUIT_PATH))
				return e
			}},
			{"load_pk", func() error {
				var e error
				p.pk, e = b.LoadPK(filepath.Join(path, worker.PK_PATH))
				return e
			}},
			{"load_vk", func() error {
				var e error
				p.vk, e = worker.ReadVerifyingKey(ecc.BN254, filepath.Join(path, worker.VK_PATH))
				return e
			}},
		} {
			if stage(r, item.name, item.fn) != nil {
				return errors.New("parameter_load_failed")
			}
		}
		keys[s.Scenario] = p
		r.emit("loaded", map[string]any{"constraints": p.cs.GetNbConstraints()})
		r.snapshot("after_load")
	}
	if *cpu != "" {
		f, e := os.OpenFile(*cpu, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return errors.New("profile_create_failed")
		}
		defer f.Close()
		if pprof.StartCPUProfile(f) != nil {
			return errors.New("cpu_profile_failed")
		}
		defer pprof.StopCPUProfile()
	}
	seq := 0
	for cycle := 0; cycle < *warmup+*cycles; cycle++ {
		for _, s := range samples {
			seq++
			r.set(context{Sample: s.ID, Scenario: s.Scenario, Sequence: seq, Warmup: cycle < *warmup})
			r.snapshot("before_proof")
			start := time.Now()
			if e := proveOne(*root, s, keys[s.Scenario], b, r); e != nil {
				return e
			}
			r.emit("proof", map[string]any{"duration_ms": float64(time.Since(start).Nanoseconds()) / 1e6, "verified": true})
			r.snapshot("after_proof")
			if *gc {
				runtime.GC()
				r.snapshot("after_explicit_gc")
			}
			if seq < len(samples)*(*warmup+*cycles) && *interval > 0 {
				time.Sleep(*interval)
				r.snapshot("after_idle")
			}
		}
	}
	if *heap != "" {
		f, e := os.OpenFile(*heap, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return errors.New("profile_create_failed")
		}
		defer f.Close()
		if pprof.WriteHeapProfile(f) != nil {
			return errors.New("heap_profile_failed")
		}
	}
	r.set(context{})
	r.snapshot("finished")
	return nil
}
func main() {
	// Existing parsers may print payloads. Discard their stdout; metrics use stderr.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		os.Exit(1)
	}
	os.Stdout = null
	r := &recorder{enc: json.NewEncoder(os.Stderr)}
	defer func() {
		if recover() != nil {
			r.emit("error", map[string]any{"code": "panic_redacted"})
			os.Exit(1)
		}
	}()
	if err := run(r); err != nil {
		r.emit("error", map[string]any{"code": err.Error()})
		os.Exit(1)
	}
}
