//go:build proverbench && msmhook && iciclemsm && cgo

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/core"
	icurve "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254"
	ig2 "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/g2"
	imsm "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/curves/bn254/msm"
	iruntime "github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/runtime"
	"github.com/ingonyama-zk/icicle-gnark/v3/wrappers/golang/runtime/config_extension"
)

// This first adapter deliberately retains no device keys or async work between
// calls. Chunking bounds input size, not the CUDA backend's total scratch space.
type icicleEngine struct {
	mu               sync.Mutex
	device           iruntime.Device
	chunk            int
	closed           bool
	internalChunks   int
	pointsMontgomery bool
	recorder         *recorder
}

var backendLoadMu sync.Mutex

func icicleError(stage string, status iruntime.EIcicleError) error {
	if status != iruntime.Success {
		return fmt.Errorf("icicle_%s_%d", stage, status)
	}
	return nil
}

func newICICLEEngine(opt backendOptions) (msmEngine, error) {
	// Native Montgomery limbs are passed directly to ICICLE. No Go pointers are
	// embedded in these structs. The pinned versions use the same LE field ABI.
	if (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") ||
		unsafe.Sizeof(fr.Element{}) != 32 || unsafe.Sizeof(fp.Element{}) != 32 ||
		unsafe.Sizeof(curve.G1Affine{}) != 64 || unsafe.Sizeof(curve.G2Affine{}) != 128 {
		return nil, errors.New("unsupported_icicle_field_layout")
	}
	if opt.Device < 0 || int64(opt.Device) > 1<<31-1 || opt.ChunkSize < 1 || opt.ChunkSize > 1<<20 {
		return nil, errors.New("invalid_icicle_options")
	}
	if !filepath.IsAbs(opt.BackendDir) {
		return nil, errors.New("absolute_backend_dir_required")
	}
	info, err := os.Stat(opt.BackendDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("invalid_backend_dir")
	}
	backendLoadMu.Lock()
	defer backendLoadMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := icicleError("load_backend", iruntime.LoadBackend(opt.BackendDir, true)); err != nil {
		return nil, err
	}
	e := &icicleEngine{device: iruntime.CreateDevice("CUDA", opt.Device), chunk: opt.ChunkSize, internalChunks: opt.InternalChunks, pointsMontgomery: true, recorder: opt.Recorder}
	if e.internalChunks == 0 {
		e.internalChunks = defaultMSMInternalChunks
	}
	if e.internalChunks < 1 || e.internalChunks > 8 {
		return nil, errors.New("invalid_internal_chunks")
	}
	if !iruntime.IsDeviceAvailable(&e.device) {
		return nil, errors.New("cuda_device_unavailable")
	}
	if err := e.selectDevice(); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *icicleEngine) selectDevice() error {
	if err := icicleError("set_device", iruntime.SetDevice(&e.device)); err != nil {
		return err
	}
	active, status := iruntime.GetActiveDevice()
	if err := icicleError("get_device", status); err != nil {
		return err
	}
	if active.GetDeviceType() != "CUDA" || active.Id != e.device.Id {
		return errors.New("cuda_device_mismatch")
	}
	return nil
}
func (e *icicleEngine) Close() { e.mu.Lock(); defer e.mu.Unlock(); e.closed = true }
func (e *icicleEngine) msmConfig() (core.MSMConfig, func()) {
	c := core.GetDefaultMSMConfig()
	c.AreScalarsMontgomeryForm = true
	c.AreBasesMontgomeryForm = e.pointsMontgomery
	c.Bitsize = fr.Bits
	c.IsAsync = false
	ext := config_extension.Create()
	// Internal pipelining is independent of the outer bounded input window.
	// Multi-stream execution requires the pinned stream-dependency/cleanup fixes.
	ext.SetInt(core.CUDA_MSM_NOF_CHUNKS, e.internalChunks)
	c.Ext = ext.AsUnsafePointer()
	return c, func() { config_extension.Delete(ext) }
}

func (e *icicleEngine) emitDetail(name string, points, chunks int, wait, exclusive, backend time.Duration, ok bool) {
	if e.recorder == nil {
		return
	}
	e.recorder.emit("msm_detail", map[string]any{
		"stage": "msm_" + name, "points": points, "chunks": chunks, "ok": ok,
		"wait_ms":          float64(wait.Nanoseconds()) / 1e6,
		"exclusive_ms":     float64(exclusive.Nanoseconds()) / 1e6,
		"backend_ms":       float64(backend.Nanoseconds()) / 1e6,
		"host_overhead_ms": float64((exclusive - backend).Nanoseconds()) / 1e6,
	})
}

func (e *icicleEngine) G1(name string, out *curve.G1Jac, p []curve.G1Affine, s []fr.Element, _ ecc.MultiExpConfig) error {
	if len(p) != len(s) {
		return errors.New("msm_length_mismatch")
	}
	waitStart := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	wait := time.Since(waitStart)
	exclusiveStart := time.Now()
	var backendTime time.Duration
	chunks, ok := 0, false
	defer func() { e.emitDetail("g1_"+name, len(s), chunks, wait, time.Since(exclusiveStart), backendTime, ok) }()
	if e.closed {
		return errors.New("icicle_engine_closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := e.selectDevice(); err != nil {
		return err
	}
	var acc curve.G1Jac
	acc.FromAffine(&curve.G1Affine{})
	cfg, cleanup := e.msmConfig()
	defer cleanup()
	for start := 0; start < len(s); start += e.chunk {
		end := min(start+e.chunk, len(s))
		result := make(core.HostSlice[icurve.Projective], 1)
		backendStart := time.Now()
		status := imsm.Msm(core.HostSlice[fr.Element](s[start:end]), core.HostSlice[curve.G1Affine](p[start:end]), &cfg, result)
		backendTime += time.Since(backendStart)
		chunks++
		// Synchronous calls return only after host inputs/results are no longer used.
		runtime.KeepAlive(p)
		runtime.KeepAlive(s)
		if err := icicleError("g1_msm", status); err != nil {
			return err
		}
		chunk, err := g1FromICICLE(&result[0])
		if err != nil {
			return err
		}
		acc.AddAssign(&chunk)
	}
	*out = acc
	ok = true
	return nil
}
func (e *icicleEngine) G2(name string, out *curve.G2Jac, p []curve.G2Affine, s []fr.Element, _ ecc.MultiExpConfig) error {
	if len(p) != len(s) {
		return errors.New("msm_length_mismatch")
	}
	waitStart := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	wait := time.Since(waitStart)
	exclusiveStart := time.Now()
	var backendTime time.Duration
	chunks, ok := 0, false
	defer func() { e.emitDetail("g2_"+name, len(s), chunks, wait, time.Since(exclusiveStart), backendTime, ok) }()
	if e.closed {
		return errors.New("icicle_engine_closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := e.selectDevice(); err != nil {
		return err
	}
	var acc curve.G2Jac
	acc.FromAffine(&curve.G2Affine{})
	cfg, cleanup := e.msmConfig()
	defer cleanup()
	for start := 0; start < len(s); start += e.chunk {
		end := min(start+e.chunk, len(s))
		result := make(core.HostSlice[ig2.G2Projective], 1)
		backendStart := time.Now()
		status := ig2.G2Msm(core.HostSlice[fr.Element](s[start:end]), core.HostSlice[curve.G2Affine](p[start:end]), &cfg, result)
		backendTime += time.Since(backendStart)
		chunks++
		runtime.KeepAlive(p)
		runtime.KeepAlive(s)
		if err := icicleError("g2_msm", status); err != nil {
			return err
		}
		chunk, err := g2FromICICLE(&result[0])
		if err != nil {
			return err
		}
		acc.AddAssign(&chunk)
	}
	*out = acc
	ok = true
	return nil
}

func decodeField(b []byte) (fp.Element, error) {
	if len(b) != 32 {
		return fp.Element{}, errors.New("invalid_icicle_field_size")
	}
	v, err := fp.LittleEndian.Element((*[32]byte)(b))
	if err != nil {
		return fp.Element{}, errors.New("invalid_icicle_field_encoding")
	}
	return v, nil
}

// ICICLE returns homogeneous (X/Z,Y/Z), gnark uses Jacobian (X/Z²,Y/Z³).
// Multiplying X by Z and Y by Z² converts without an inversion.
func g1FromICICLE(p *icurve.Projective) (curve.G1Jac, error) {
	var out curve.G1Jac
	x, err := decodeField(p.X.ToBytesLittleEndian())
	if err != nil {
		return out, err
	}
	y, err := decodeField(p.Y.ToBytesLittleEndian())
	if err != nil {
		return out, err
	}
	z, err := decodeField(p.Z.ToBytesLittleEndian())
	if err != nil {
		return out, err
	}
	if z.IsZero() {
		out.FromAffine(&curve.G1Affine{})
		return out, nil
	}
	z2 := z
	z2.Square(&z)
	out.X.Mul(&x, &z)
	out.Y.Mul(&y, &z2)
	out.Z = z
	if !out.IsOnCurve() {
		return curve.G1Jac{}, errors.New("invalid_icicle_g1_result")
	}
	return out, nil
}
func g2FromICICLE(p *ig2.G2Projective) (curve.G2Jac, error) {
	var out curve.G2Jac
	fields := []*fp.Element{&out.X.A0, &out.X.A1, &out.Y.A0, &out.Y.A1, &out.Z.A0, &out.Z.A1}
	coords := [][]byte{p.X.ToBytesLittleEndian(), p.Y.ToBytesLittleEndian(), p.Z.ToBytesLittleEndian()}
	for i, b := range coords {
		if len(b) != 64 {
			return out, errors.New("invalid_icicle_g2_size")
		}
		for j := 0; j < 2; j++ {
			v, err := decodeField(b[j*32 : (j+1)*32])
			if err != nil {
				return out, err
			}
			*fields[2*i+j] = v
		}
	}
	if out.Z.IsZero() {
		out.FromAffine(&curve.G2Affine{})
		return out, nil
	}
	x, y, z := out.X, out.Y, out.Z
	z2 := z
	z2.Square(&z)
	out.X.Mul(&x, &z)
	out.Y.Mul(&y, &z2)
	if !out.IsOnCurve() {
		return curve.G2Jac{}, errors.New("invalid_icicle_g2_result")
	}
	return out, nil
}
