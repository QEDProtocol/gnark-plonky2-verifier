//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/fft"
	"github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/core"
	icurve "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254"
	intt "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/ntt"
)

// NTT has no Montgomery-input flag. Convert explicitly and derive its root
// from the existing gnark domain so both libraries use the same primitive root.
func canonicalNTTScalar(v fr.Element) icurve.ScalarField {
	words := v.Bits()
	var limbs [8]uint32
	for i, w := range words {
		limbs[2*i], limbs[2*i+1] = uint32(w), uint32(w>>32)
	}
	var out icurve.ScalarField
	out.FromLimbs(limbs[:])
	return out
}

func TestICICLENTTVectors(t *testing.T) {
	if os.Getenv("PROVERBENCH_GPU_TEST") != "1" || os.Getenv("PROVERBENCH_NTT_TEST") != "1" {
		t.Skip("explicit NTT compatibility check required")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	native, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: defaultMSMChunkSize, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	const largest = 65536
	domain := fft.NewDomain(largest)
	if err := icicleError("ntt_init", intt.InitDomain(canonicalNTTScalar(domain.Generator), core.GetDefaultNTTInitDomainConfig())); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := icicleError("ntt_release", intt.ReleaseDomain()); err != nil {
			t.Error(err)
		}
	}()
	for _, n := range []int{16, 1024, largest} {
		for _, inverse := range []bool{false, true} {
			for _, coset := range []bool{false, true} {
				t.Run(fmt.Sprintf("n_%d/inverse_%t/coset_%t", n, inverse, coset), func(t *testing.T) {
					// t.Run uses a separate goroutine; select the CUDA device on
					// its locked OS thread as well as the domain owner's thread.
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					if err := native.(*icicleEngine).selectDevice(); err != nil {
						t.Fatal(err)
					}
					d := fft.NewDomain(uint64(n))
					want := make([]fr.Element, n)
					input, output := make(core.HostSlice[icurve.ScalarField], n), make(core.HostSlice[icurve.ScalarField], n)
					for i := range want {
						want[i].SetUint64(uint64(i + 2))
						want[i].Inverse(&want[i])
						if i%17 == 0 {
							want[i].SetZero()
						}
						input[i] = canonicalNTTScalar(want[i])
					}
					cfg := intt.GetDefaultNttConfig()
					cfg.Ordering, cfg.IsAsync = core.KNN, false
					var options []fft.Option
					if coset {
						gen := canonicalNTTScalar(d.FrMultiplicativeGen)
						copy(cfg.CosetGen[:], gen.GetLimbs())
						options = append(options, fft.OnCoset())
					}
					direction := core.KForward
					if inverse {
						direction = core.KInverse
						d.FFTInverse(want, fft.DIF, options...)
					} else {
						d.FFT(want, fft.DIF, options...)
					}
					fft.BitReverse(want) // gnark DIF outputs bit-reversed order; KNN is natural order.
					status := intt.Ntt(input, direction, &cfg, output)
					runtime.KeepAlive(input)
					runtime.KeepAlive(output)
					if err := icicleError("ntt", status); err != nil {
						t.Fatal(err)
					}
					for i := range want {
						bytes := output[i].ToBytesLittleEndian()
						got, err := fr.LittleEndian.Element((*[32]byte)(bytes))
						if err != nil || !got.Equal(&want[i]) {
							t.Fatalf("CPU/GPU NTT mismatch at index %d", i)
						}
					}
				})
			}
		}
	}
}
