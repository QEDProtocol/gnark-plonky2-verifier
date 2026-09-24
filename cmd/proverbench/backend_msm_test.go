//go:build proverbench && msmhook

package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	curve "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/zilong-dai/gnark/backend/groth16"
	"github.com/zilong-dai/gnark/frontend"
	"github.com/zilong-dai/gnark/frontend/cs/r1cs"
)

// Synthetic keys only. Compilation of this file does not execute Setup/Prove.
func TestMSMHookProofAndErrorJoin(t *testing.T) {
	cs, err := frontend.Compile(ecc.BN254.ScalarField(), r1cs.NewBuilder, &cubic{})
	if err != nil {
		t.Fatal(err)
	}
	pk, vk, err := groth16.Setup(cs)
	if err != nil {
		t.Fatal(err)
	}
	w, err := frontend.NewWitness(&cubic{X: 3, Y: 27}, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := w.Public()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("native_cpu_verifier", func(t *testing.T) {
		b, err := selectBackend("cpu-msm")
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		p, err := b.Prove(cs, pk, w)
		if err != nil {
			t.Fatal(err)
		}
		if err := groth16.Verify(p, vk, pub); err != nil {
			t.Fatal(err)
		}
		wrong, err := frontend.NewWitness(&cubic{Y: 28}, ecc.BN254.ScalarField(), frontend.PublicOnly())
		if err != nil {
			t.Fatal(err)
		}
		if groth16.Verify(p, vk, wrong) == nil {
			t.Fatal("incorrect public input verified")
		}
	})
	t.Run("join_all_msm_on_error", func(t *testing.T) {
		engine := &failingMSM{started: make(chan struct{}), released: make(chan struct{})}
		b := &msmBackend{name: "injected_failure", engine: engine}
		defer b.Close()
		if _, err := b.Prove(cs, pk, w); !errors.Is(err, errInjectedMSM) {
			t.Fatalf("unexpected error: %v", err)
		}
		if engine.active.Load() != 0 || engine.completed.Load() != 4 {
			t.Fatalf("proof returned before all G1 tasks joined: active=%d completed=%d", engine.active.Load(), engine.completed.Load())
		}
	})
}

var errInjectedMSM = errors.New("injected_msm_failure")

type failingMSM struct {
	once              sync.Once
	started, released chan struct{}
	active, completed atomic.Int32
}

func (*failingMSM) Close() {}
func (e *failingMSM) G1(_ string, _ *curve.G1Jac, _ []curve.G1Affine, _ []fr.Element, _ ecc.MultiExpConfig) error {
	e.active.Add(1)
	defer e.active.Add(-1)
	defer e.completed.Add(1)
	e.once.Do(func() { close(e.started) })
	<-e.released
	time.Sleep(10 * time.Millisecond)
	return errInjectedMSM
}
func (e *failingMSM) G2(_ string, _ *curve.G2Jac, _ []curve.G2Affine, _ []fr.Element, _ ecc.MultiExpConfig) error {
	<-e.started
	close(e.released)
	return errInjectedMSM
}
