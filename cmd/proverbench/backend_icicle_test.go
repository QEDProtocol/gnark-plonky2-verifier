//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	icurve "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254"
	ig2 "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/g2"
)

// Only the explicit gpu-check runner enables this test. Ordinary unit tests
// neither discover nor reserve GPUs.
func TestICICLEMSMVectors(t *testing.T) {
	if os.Getenv("PROVERBENCH_GPU_TEST") != "1" {
		t.Skip("explicit GPU check required")
	}
	_, _, gen1, gen2 := curve.Generators()
	for _, chunk := range []int{1, 4, 64} {
		t.Run(fmt.Sprintf("chunk_%d", chunk), func(t *testing.T) {
			e, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: chunk, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			for _, n := range []int{0, 1, 2, 7, 16, 17} {
				for _, zero := range []bool{false, true} {
					p1, p2, scalars := make([]curve.G1Affine, n), make([]curve.G2Affine, n), make([]fr.Element, n)
					for i := 0; i < n; i++ {
						p1[i], p2[i] = gen1, gen2
						if i%5 == 4 {
							p1[i] = curve.G1Affine{}
							p2[i] = curve.G2Affine{}
						}
						if !zero {
							scalars[i].SetUint64(uint64(i + 1))
							// Cover full-width Montgomery scalars as well as tiny values.
							if i%3 == 0 {
								scalars[i].Neg(&scalars[i])
							}
							if i%3 == 2 {
								scalars[i].SetZero()
							}
						}
					}
					var want1, got1 curve.G1Jac
					var want2, got2 curve.G2Jac
					want1.FromAffine(&curve.G1Affine{})
					want2.FromAffine(&curve.G2Affine{})
					cfg := ecc.MultiExpConfig{NbTasks: 1}
					if n > 0 {
						if _, err := want1.MultiExp(p1, scalars, cfg); err != nil {
							t.Fatal(err)
						}
						if _, err := want2.MultiExp(p2, scalars, cfg); err != nil {
							t.Fatal(err)
						}
					}
					if err := e.G1("test", &got1, p1, scalars, cfg); err != nil {
						t.Fatal(err)
					}
					if err := e.G2("test", &got2, p2, scalars, cfg); err != nil {
						t.Fatal(err)
					}
					var a, b curve.G1Affine
					a.FromJacobian(&got1)
					b.FromJacobian(&want1)
					if !a.Equal(&b) {
						t.Fatalf("G1 mismatch n=%d zero=%v", n, zero)
					}
					var c, d curve.G2Affine
					c.FromJacobian(&got2)
					d.FromJacobian(&want2)
					if !c.Equal(&d) {
						t.Fatalf("G2 mismatch n=%d zero=%v", n, zero)
					}
				}
			}
			e.Close()
			var out curve.G1Jac
			if e.G1("closed", &out, nil, nil, ecc.MultiExpConfig{}) == nil {
				t.Fatal("closed backend accepted work")
			}
		})
	}
}

func fieldBytes(v fp.Element) []byte {
	var b [32]byte
	fp.LittleEndian.PutElement(&b, v)
	return b[:]
}

func TestICICLEMSMDiverseBases(t *testing.T) {
	if os.Getenv("PROVERBENCH_GPU_TEST") != "1" {
		t.Skip("explicit GPU check required")
	}
	_, _, g1, g2 := curve.Generators()
	for _, n := range []int{17, 1024, 65537} {
		t.Run(fmt.Sprintf("points_%d", n), func(t *testing.T) {
			s := make([]fr.Element, n)
			for i := range s {
				s[i].SetUint64(uint64(i + 2))
				s[i].Inverse(&s[i])
			}
			p1 := curve.BatchScalarMultiplicationG1(&g1, s)
			p2 := curve.BatchScalarMultiplicationG2(&g2, s)
			for i := range s {
				s[i].Square(&s[i])
			}
			e, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: 65536, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			var a, b curve.G1Jac
			var c, d curve.G2Jac
			cfg := ecc.MultiExpConfig{NbTasks: 2}
			if _, err := b.MultiExp(p1, s, cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := d.MultiExp(p2, s, cfg); err != nil {
				t.Fatal(err)
			}
			if err := e.G1("test", &a, p1, s, cfg); err != nil {
				t.Fatal(err)
			}
			if !a.Equal(&b) {
				t.Fatal("G1 diverse bases mismatch")
			}
			if err := e.G2("test", &c, p2, s, cfg); err != nil {
				t.Fatal(err)
			}
			if !c.Equal(&d) {
				t.Fatal("G2 diverse bases mismatch")
			}
		})
	}
}

// Pure coordinate conversion tests; no device discovery or CUDA calls.
func TestICICLEHomogeneousConversion(t *testing.T) {
	_, _, g1, g2 := curve.Generators()
	var scale fp.Element
	scale.SetUint64(7)
	x, y := g1.X, g1.Y
	x.Mul(&x, &scale)
	y.Mul(&y, &scale)
	var p icurve.Projective
	p.X.FromBytesLittleEndian(fieldBytes(x))
	p.Y.FromBytesLittleEndian(fieldBytes(y))
	p.Z.FromBytesLittleEndian(fieldBytes(scale))
	got, err := g1FromICICLE(&p)
	if err != nil {
		t.Fatal(err)
	}
	var a curve.G1Affine
	a.FromJacobian(&got)
	if !a.Equal(&g1) {
		t.Fatal("G1 homogeneous conversion mismatch")
	}
	qx, qy, qz := g2.X, g2.Y, g2.X
	qz.SetOne()
	qz.A0 = scale
	qx.Mul(&qx, &qz)
	qy.Mul(&qy, &qz)
	var q ig2.G2Projective
	q.X.FromBytesLittleEndian(append(fieldBytes(qx.A0), fieldBytes(qx.A1)...))
	q.Y.FromBytesLittleEndian(append(fieldBytes(qy.A0), fieldBytes(qy.A1)...))
	q.Z.FromBytesLittleEndian(append(fieldBytes(qz.A0), fieldBytes(qz.A1)...))
	got2, err := g2FromICICLE(&q)
	if err != nil {
		t.Fatal(err)
	}
	var a2 curve.G2Affine
	a2.FromJacobian(&got2)
	if !a2.Equal(&g2) {
		t.Fatal("G2 homogeneous conversion mismatch")
	}
	p.Z.Zero()
	got, err = g1FromICICLE(&p)
	if err != nil || !got.Z.IsZero() {
		t.Fatal("G1 infinity mismatch")
	}
	q.Z.Zero()
	got2, err = g2FromICICLE(&q)
	if err != nil || !got2.Z.IsZero() {
		t.Fatal("G2 infinity mismatch")
	}
}
