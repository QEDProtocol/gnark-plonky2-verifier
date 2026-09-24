//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/fft"
	native "github.com/zilong-dai/gnark/backend/groth16/bn254"
)

func TestICICLEComputeH(t *testing.T) {
	mode := os.Getenv("PROVERBENCH_H_TEST")
	if os.Getenv("PROVERBENCH_GPU_TEST") != "1" || mode == "" {
		t.Skip("explicit computeH check required")
	}
	engine, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: defaultMSMChunkSize, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	e := engine.(*icicleEngine)
	var metrics bytes.Buffer
	e.recorder = &recorder{enc: json.NewEncoder(&metrics)}
	// Force a failure after domain/device allocation, then use the same engine
	// successfully below. This exercises cleanup, not just input validation.
	invalid := fft.NewDomain(16)
	invalid.FrMultiplicativeGen.SetOne()
	if _, err := e.ComputeH(nil, nil, nil, invalid); err == nil {
		t.Fatal("invalid coset accepted")
	}
	sizes := []int{16, 1024, 65536, 1024}
	if mode == "large" {
		sizes = []int{1 << 22, 1 << 23, 1 << 22}
	}
	for trial, n := range sizes {
		t.Run(fmt.Sprintf("trial_%d/n_%d", trial, n), func(t *testing.T) {
			d := fft.NewDomain(uint64(n))
			cases := []int{0, n - 3, n}
			if mode == "large" {
				cases = []int{n - 3}
			}
			for _, length := range cases {
				t.Run(fmt.Sprintf("length_%d", length), func(t *testing.T) {
					a, b, c := make([]fr.Element, length), make([]fr.Element, length), make([]fr.Element, length)
					var seed, step fr.Element
					seed.SetUint64(7)
					seed.Inverse(&seed)
					step.SetUint64(11)
					step.Inverse(&step)
					for i := range a {
						seed.Mul(&seed, &step)
						a[i] = seed
						seed.Mul(&seed, &step)
						b[i] = seed
						seed.Mul(&seed, &step)
						c[i] = seed
						if i%17 == 0 {
							a[i].SetZero()
						}
					}
					metrics.Reset()
					start := time.Now()
					got, err := e.ComputeH(a, b, c, d)
					t.Logf("h_metrics %s", metrics.String())
					gpuMS := float64(time.Since(start).Nanoseconds()) / 1e6
					if err != nil {
						t.Fatal(err)
					}
					start = time.Now()
					// The pinned implementation is the oracle, not a duplicate of
					// the GPU algorithm. It is allowed to consume input buffers.
					want, err := native.ComputeHCPU(a, b, c, d)
					cpuMS := float64(time.Since(start).Nanoseconds()) / 1e6
					if err != nil {
						t.Fatal(err)
					}
					if len(got) != len(want) {
						t.Fatal("H output length mismatch")
					}
					for i := range want {
						if !got[i].Equal(&want[i]) {
							t.Fatalf("CPU/GPU H mismatch at index %d", i)
						}
					}
					t.Logf("compute_h_check n=%d input=%d gpu_ms=%.3f cpu_ms=%.3f verified=true", n, length, gpuMS, cpuMS)
				})
			}
		})
	}
	if _, err := e.ComputeH(make([]fr.Element, 2), nil, nil, fft.NewDomain(16)); err == nil {
		t.Fatal("unequal inputs accepted")
	}
	engine.Close()
	if _, err := e.ComputeH(nil, nil, nil, fft.NewDomain(16)); err == nil {
		t.Fatal("closed engine accepted")
	}
}
