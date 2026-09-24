//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"errors"
	"math/big"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/fft"
	"github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/core"
	icurve "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254"
	intt "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/ntt"
	ivec "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/vecOps"
	iruntime "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/runtime"
)

// NTT/vector ops use canonical scalars, unlike the MSM Montgomery input path.
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

// ICICLE's NTT domain is shared by engines on a device. Serialize its whole
// lifetime across engines; release it on every call, including error paths.
var hDomainMu sync.Mutex

// Bound host conversion parallelism independently of CUDA execution. Workers
// operate on disjoint elements and are joined before buffers reach CUDA.
func parallelH(n int, fn func(int, int) error) error {
	workers := min(8, runtime.GOMAXPROCS(0))
	if n < 65536 {
		return fn(0, n)
	}
	var jobs sync.WaitGroup
	errs := make([]error, workers)
	for worker := 0; worker < workers; worker++ {
		jobs.Add(1)
		go func(w int) { defer jobs.Done(); errs[w] = fn(n*w/workers, n*(w+1)/workers) }(worker)
	}
	jobs.Wait()
	return errors.Join(errs...)
}

func (e *icicleEngine) ComputeH(a, b, c []fr.Element, domain *fft.Domain) (out []fr.Element, err error) {
	if domain == nil || domain.Cardinality < 2 || domain.Cardinality > 1<<23 ||
		domain.Cardinality&(domain.Cardinality-1) != 0 || len(a) != len(b) || len(a) != len(c) || uint64(len(a)) > domain.Cardinality {
		return nil, errors.New("invalid_h_input")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("icicle_engine_closed")
	}
	hDomainMu.Lock()
	defer hDomainMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = e.selectDevice(); err != nil {
		return nil, err
	}
	start := time.Now()
	phaseStart := start
	phases := make(map[string]float64)
	mark := func(name string) {
		now := time.Now()
		phases[name] += float64(now.Sub(phaseStart).Nanoseconds()) / 1e6
		phaseStart = now
	}
	defer func() {
		if e.recorder != nil {
			e.recorder.emit("h_detail", map[string]any{"points": domain.Cardinality, "duration_ms": float64(time.Since(start).Nanoseconds()) / 1e6, "ok": err == nil, "phases_ms": phases})
		}
	}()
	defer func() {
		err = errors.Join(err, icicleError("h_release", intt.ReleaseDomain()))
		mark("domain_release")
		if err != nil {
			out = nil
		}
	}()
	// Register cleanup before InitDomain too: an allocation failure can leave
	// a partially built domain in the native per-device state.
	if err = icicleError("h_init", intt.InitDomain(canonicalNTTScalar(domain.Generator), core.GetDefaultNTTInitDomainConfig())); err != nil {
		return nil, err
	}
	mark("domain_init")
	n := int(domain.Cardinality)
	var dev [3]core.DeviceSlice
	allocated := 0
	defer func() {
		for i := allocated - 1; i >= 0; i-- {
			err = errors.Join(err, icicleError("h_free", dev[i].Free()))
		}
		mark("device_free")
		if err != nil {
			out = nil
		}
	}()
	// One reusable host buffer; three device vectors. Keep all seven transforms
	// and pointwise operations on device, with synchronous calls throughout.
	host := make(core.HostSlice[icurve.ScalarField], n)
	for i, input := range [][]fr.Element{a, b, c} {
		if _, status := dev[i].Malloc(32, n); status != iruntime.Success {
			return nil, icicleError("h_alloc", status)
		}
		allocated++
		clear(host)
		_ = parallelH(len(input), func(start, end int) error {
			for j := start; j < end; j++ {
				host[j] = canonicalNTTScalar(input[j])
			}
			return nil
		})
		mark("encode_and_allocate")
		_, status := iruntime.CopyToDevice(dev[i].AsUnsafePointer(), host.AsUnsafePointer(), uint(n*32))
		runtime.KeepAlive(host)
		if err = icicleError("h_upload", status); err != nil {
			return nil, err
		}
		mark("upload")
	}
	transform := func(i int, direction core.NTTDir, ordering core.Ordering, coset bool) error {
		cfg := intt.GetDefaultNttConfig()
		cfg.Ordering, cfg.IsAsync = ordering, false
		if coset {
			gen := canonicalNTTScalar(domain.FrMultiplicativeGen)
			copy(cfg.CosetGen[:], gen.GetLimbs())
		}
		status := intt.Ntt(dev[i], direction, &cfg, dev[i])
		mark("ntt")
		return icicleError("h_ntt", status)
	}
	for i := range dev {
		if err = transform(i, core.KInverse, core.KNR, false); err != nil {
			return nil, err
		}
	}
	for i := range dev {
		if err = transform(i, core.KForward, core.KRN, true); err != nil {
			return nil, err
		}
	}
	cfg := core.DefaultVecOpsConfig()
	if err = icicleError("h_mul", ivec.VecOp(dev[0], dev[1], dev[0], cfg, core.Mul)); err != nil {
		return nil, err
	}
	if err = icicleError("h_sub", ivec.VecOp(dev[0], dev[2], dev[0], cfg, core.Sub)); err != nil {
		return nil, err
	}
	mark("pointwise")
	var den, one fr.Element
	one.SetOne()
	den.Exp(domain.FrMultiplicativeGen, new(big.Int).SetUint64(domain.Cardinality))
	den.Sub(&den, &one)
	if den.IsZero() {
		return nil, errors.New("invalid_h_coset")
	}
	den.Inverse(&den)
	scalar := canonicalNTTScalar(den)
	for i := range host {
		host[i] = scalar
	}
	// B is no longer needed. Reuse it for the denominator vector, avoiding an
	// additional allocation or an unreviewed custom CUDA kernel/ABI binding.
	_, status := iruntime.CopyToDevice(dev[1].AsUnsafePointer(), host.AsUnsafePointer(), uint(n*32))
	runtime.KeepAlive(host)
	if err = icicleError("h_upload_den", status); err != nil {
		return nil, err
	}
	if err = icicleError("h_scale", ivec.VecOp(dev[0], dev[1], dev[0], cfg, core.Mul)); err != nil {
		return nil, err
	}
	mark("denominator_and_scale")
	if err = transform(0, core.KInverse, core.KNR, true); err != nil {
		return nil, err
	}
	_, status = iruntime.CopyFromDevice(host.AsUnsafePointer(), dev[0].AsUnsafePointer(), uint(n*32))
	runtime.KeepAlive(host)
	if err = icicleError("h_download", status); err != nil {
		return nil, err
	}
	mark("download")
	out = make([]fr.Element, n)
	err = parallelH(n, func(start, end int) error {
		for i := start; i < end; i++ {
			// Same fixed little-endian limb ABI as the MSM bridge, without a
			// temporary byte allocation per element.
			v, decodeErr := fr.LittleEndian.Element((*[32]byte)(unsafe.Pointer(&host[i])))
			if decodeErr != nil {
				return errors.New("invalid_h_scalar_encoding")
			}
			out[i] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	runtime.KeepAlive(host)
	mark("decode")
	return out, nil
}
