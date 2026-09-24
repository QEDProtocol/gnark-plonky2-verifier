//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"os"
	"runtime"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr/fft"
	"github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/core"
	intt "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/ntt"
	iruntime "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/runtime"
)

func TestICICLENTTDomainMemory(t *testing.T) {
	if os.Getenv("PROVERBENCH_GPU_TEST") != "1" || os.Getenv("PROVERBENCH_DOMAIN_TEST") != "1" {
		t.Skip("explicit domain lifecycle test required")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := newICICLEEngine(backendOptions{Device: 0, ChunkSize: defaultMSMChunkSize, BackendDir: os.Getenv("PROVERBENCH_BACKEND_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	hDomainMu.Lock()
	defer hDomainMu.Unlock()
	d := fft.NewDomain(1 << 20)
	var baseline uint
	for i := 0; i < 21; i++ {
		if err := icicleError("domain_test_init", intt.InitDomain(canonicalNTTScalar(d.Generator), core.GetDefaultNTTInitDomainConfig())); err != nil {
			t.Fatal(err)
		}
		if err := icicleError("domain_test_release", intt.ReleaseDomain()); err != nil {
			t.Fatal(err)
		}
		if err := icicleError("domain_test_sync", iruntime.DeviceSynchronize()); err != nil {
			t.Fatal(err)
		}
		mem, status := iruntime.GetAvailableMemory()
		if err := icicleError("domain_test_memory", status); err != nil {
			t.Fatal(err)
		}
		if i <= 4 {
			baseline = mem.Free
		}
		t.Logf("domain_memory iteration=%d free_bytes=%d retained_since_warmup=%d", i, mem.Free, int64(baseline)-int64(mem.Free))
		// Allow pool/managed-memory accounting variation; a 32 MiB leak per
		// domain accumulates 512 MiB across the 16 measured calls.
		if i == 20 && int64(baseline)-int64(mem.Free) > 128<<20 {
			t.Fatal("NTT domain release retains more than 128 MiB after sixteen repeated calls")
		}
	}
}
