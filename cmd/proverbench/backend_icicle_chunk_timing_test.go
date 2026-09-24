//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
)

// Measure transfer + conversion + MSM + result conversion, with balanced chunk
// order across rounds. This is not a kernel-only or complete-prover benchmark.
func TestICICLEMSMChunkTiming(t *testing.T) {
	if os.Getenv("PROVERBENCH_CHUNK_TIMING") != "1" || os.Getenv("PROVERBENCH_GPU_TEST") != "1" {
		t.Skip("explicit chunk timing required")
	}
	const largest = 1 << 20
	scalars, p1, p2 := pipelineVectors(largest)
	native, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: largest, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	engine := native.(*icicleEngine)
	cfg := ecc.MultiExpConfig{NbTasks: 2}
	chunks := []int{1, 2, 4, 8}
	for _, n := range []int{1 << 18, largest} {
		var want1 curve.G1Jac
		var want2 curve.G2Jac
		if _, err := want1.MultiExp(p1[:n], scalars[:n], cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := want2.MultiExp(p2[:n], scalars[:n], cfg); err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 9; round++ {
			for position := range chunks {
				// Each choice occupies every position twice in measured rounds.
				count := chunks[(position+round)%len(chunks)]
				engine.internalChunks = count
				for _, group := range []string{"g1", "g2"} {
					begin := time.Now()
					var callErr error
					var got1 curve.G1Jac
					var got2 curve.G2Jac
					if group == "g1" {
						callErr = engine.G1("chunk_timing", &got1, p1[:n], scalars[:n], cfg)
					} else {
						callErr = engine.G2("chunk_timing", &got2, p2[:n], scalars[:n], cfg)
					}
					elapsed := time.Since(begin)
					correct := callErr == nil && ((group == "g1" && got1.Equal(&want1)) || (group == "g2" && got2.Equal(&want2)))
					row := map[string]any{"points": n, "chunks": count, "round": round, "position": position, "warmup": round == 0, "group": group, "duration_ms": float64(elapsed.Nanoseconds()) / 1e6, "correct": correct}
					if callErr != nil {
						row["error_code"] = msmErrorCode(callErr)
					}
					encoded, _ := json.Marshal(row)
					t.Log(string(encoded))
					if !correct {
						t.Fatal("chunk timing result differs from CPU")
					}
				}
			}
		}
	}
}
