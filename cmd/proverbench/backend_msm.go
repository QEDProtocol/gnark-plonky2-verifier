//go:build proverbench && msmhook

package main

import (
	"errors"
	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/zilong-dai/gnark/backend/groth16"
	native "github.com/zilong-dai/gnark/backend/groth16/bn254"
	"github.com/zilong-dai/gnark/backend/witness"
	"github.com/zilong-dai/gnark/constraint"
	cs "github.com/zilong-dai/gnark/constraint/bn254"
	"strconv"
	"strings"
	"time"
)

type msmEngine interface {
	native.MSMBackend
	Close()
}
type msmBackend struct {
	cpuBackend
	name   string
	engine msmEngine
}

func (b *msmBackend) Name() string { return b.name }
func (b *msmBackend) Close()       { b.engine.Close() }
func (b *msmBackend) Prove(c constraint.ConstraintSystem, p groth16.ProvingKey, w witness.Witness) (groth16.Proof, error) {
	r, ok := c.(*cs.R1CS)
	if !ok {
		return nil, errors.New("unsupported_circuit_type")
	}
	pk, ok := p.(*native.ProvingKey)
	if !ok {
		return nil, errors.New("unsupported_key_type")
	}
	return native.ProveWithMSM(r, pk, w, b.engine)
}
func newMSMBackend(name string, opt backendOptions) (proverBackend, error) {
	var engine msmEngine = cpuMSM{}
	if name == "icicle-msm" {
		var e error
		engine, e = newICICLEEngine(opt)
		if e != nil {
			return nil, e
		}
	}
	return &msmBackend{name: name, engine: &measuredMSM{inner: engine, r: opt.Recorder}}, nil
}

type cpuMSM struct{}

func (cpuMSM) Close() {}
func (cpuMSM) G1(_ string, out *curve.G1Jac, p []curve.G1Affine, s []fr.Element, c ecc.MultiExpConfig) error {
	_, e := out.MultiExp(p, s, c)
	return e
}
func (cpuMSM) G2(_ string, out *curve.G2Jac, p []curve.G2Affine, s []fr.Element, c ecc.MultiExpConfig) error {
	_, e := out.MultiExp(p, s, c)
	return e
}

type measuredMSM struct {
	inner msmEngine
	r     *recorder
}

func (m *measuredMSM) Close() { m.inner.Close() }
func (m *measuredMSM) emit(name string, n int, t time.Time, e error) {
	if m.r != nil {
		f := map[string]any{"stage": "msm_" + name, "points": n, "duration_ms": float64(time.Since(t).Nanoseconds()) / 1e6, "ok": e == nil}
		if e != nil {
			f["error_code"] = msmErrorCode(e)
		}
		m.r.emit("stage", f)
	}
}

func msmErrorCode(e error) string {
	code := e.Error()
	switch code {
	case "invalid_icicle_g1_result", "invalid_icicle_g2_result", "invalid_icicle_field_encoding", "invalid_icicle_field_size", "invalid_icicle_g2_size", "msm_length_mismatch", "cuda_device_mismatch", "icicle_engine_closed":
		return code
	}
	for _, prefix := range []string{"icicle_g1_msm_", "icicle_g2_msm_", "icicle_set_device_", "icicle_get_device_"} {
		if strings.HasPrefix(code, prefix) {
			if _, err := strconv.Atoi(strings.TrimPrefix(code, prefix)); err == nil {
				return code
			}
		}
	}
	return "msm_error_redacted"
}
func (m *measuredMSM) G1(name string, out *curve.G1Jac, p []curve.G1Affine, s []fr.Element, c ecc.MultiExpConfig) error {
	t := time.Now()
	e := m.inner.G1(name, out, p, s, c)
	m.emit("g1_"+name, len(s), t, e)
	return e
}
func (m *measuredMSM) G2(name string, out *curve.G2Jac, p []curve.G2Affine, s []fr.Element, c ecc.MultiExpConfig) error {
	t := time.Now()
	e := m.inner.G2(name, out, p, s, c)
	m.emit("g2_"+name, len(s), t, e)
	return e
}
