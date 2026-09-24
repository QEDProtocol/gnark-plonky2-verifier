//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func pipelineVectors(size int) ([]fr.Element, []curve.G1Affine, []curve.G2Affine) {
	_, _, g1, g2 := curve.Generators()
	scalars := make([]fr.Element, size)
	for i := range scalars {
		scalars[i].SetUint64(uint64(i + 2))
		scalars[i].Inverse(&scalars[i])
	}
	p1 := curve.BatchScalarMultiplicationG1(&g1, scalars)
	p2 := curve.BatchScalarMultiplicationG2(&g2, scalars)
	for i := range scalars {
		scalars[i].Square(&scalars[i])
		if i%31 == 0 {
			p1[i] = curve.G1Affine{}
			p2[i] = curve.G2Affine{}
		}
		if i%13 == 0 {
			scalars[i].SetZero()
		}
	}
	return scalars, p1, p2
}

// This matrix distinguishes the input conversion stream from the MSM math.
// Scalars remain Montgomery in both modes; only point encoding changes.
func TestICICLEMSMPipeline(t *testing.T) {
	if os.Getenv("PROVERBENCH_PIPELINE") != "1" || os.Getenv("PROVERBENCH_GPU_TEST") != "1" {
		t.Skip("explicit pipeline matrix required")
	}
	const largest = 262145
	scalars, p1, p2 := pipelineVectors(largest)
	canonical1 := append([]curve.G1Affine(nil), p1...)
	canonical2 := append([]curve.G2Affine(nil), p2...)
	for i := range canonical1 {
		p := &canonical1[i]
		p.X = fp.Element(p.X.Bits())
		p.Y = fp.Element(p.Y.Bits())
		q := &canonical2[i]
		q.X.A0 = fp.Element(q.X.A0.Bits())
		q.X.A1 = fp.Element(q.X.A1.Bits())
		q.Y.A0 = fp.Element(q.Y.A0.Bits())
		q.Y.A1 = fp.Element(q.Y.A1.Bits())
	}
	native, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: largest, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	engine := native.(*icicleEngine)
	cfg := ecc.MultiExpConfig{NbTasks: 2}
	failures := 0
	for _, n := range []int{1024, 65536, 65537, 262144, 262145} {
		var want1 curve.G1Jac
		var want2 curve.G2Jac
		if _, err := want1.MultiExp(p1[:n], scalars[:n], cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := want2.MultiExp(p2[:n], scalars[:n], cfg); err != nil {
			t.Fatal(err)
		}
		for _, mont := range []bool{true, false} {
			engine.pointsMontgomery = mont
			points1, points2 := p1, p2
			if !mont {
				points1, points2 = canonical1, canonical2
			}
			for _, chunks := range []int{1, 2, 4, 8, 0} {
				engine.internalChunks = chunks // 0 asks ICICLE to choose its default.
				for repeat := 0; repeat < 4; repeat++ {
					for _, group := range []string{"g1", "g2"} {
						begin := time.Now()
						var callErr error
						equal := false
						if group == "g1" {
							var got curve.G1Jac
							callErr = engine.G1("matrix", &got, points1[:n], scalars[:n], cfg)
							equal = callErr == nil && got.Equal(&want1)
						} else {
							var got curve.G2Jac
							callErr = engine.G2("matrix", &got, points2[:n], scalars[:n], cfg)
							equal = callErr == nil && got.Equal(&want2)
						}
						row := map[string]any{"points": n, "montgomery_points": mont, "chunks": chunks, "repeat": repeat, "warmup": repeat == 0, "group": group, "duration_ms": float64(time.Since(begin).Nanoseconds()) / 1e6, "correct": equal}
						if callErr != nil {
							row["error_code"] = msmErrorCode(callErr)
						}
						if !equal {
							failures++
						}
						encoded, _ := json.Marshal(row)
						t.Log(string(encoded))
					}
				}
			}
		}
	}
	if failures != 0 {
		t.Fatalf("pipeline matrix: %d incorrect results", failures)
	}
}
