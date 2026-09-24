//go:build proverbench

package main

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/zilong-dai/gnark/backend/groth16"
	"github.com/zilong-dai/gnark/frontend"
	"github.com/zilong-dai/gnark/frontend/cs/r1cs"
)

func TestRejectUnsupportedBackend(t *testing.T) {
	if _, e := selectBackend("icicle"); e == nil {
		t.Fatal("GPU must not silently fall back to CPU")
	}
}
func TestSampleSelection(t *testing.T) {
	m := manifest{Samples: []sample{{ID: "bridge-00", Scenario: "bridge"}, {ID: "deposit-00", Scenario: "deposit"}}}
	s, e := selectSamples(m, "deposit", "")
	if e != nil || len(s) != 1 || s[0].ID != "deposit-00" {
		t.Fatal(s, e)
	}
	if _, e = selectSamples(m, "all", "missing"); e == nil {
		t.Fatal("empty selection accepted")
	}
	for _, id := range []string{"", ".", "..", "../secret", "a/b", "a\\b"} {
		if _, e = selectSamples(manifest{Samples: []sample{{ID: id, Scenario: "bridge"}}}, "all", ""); e == nil {
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
}

type cubic struct {
	X frontend.Variable
	Y frontend.Variable `gnark:",public"`
}

func (c *cubic) Define(api frontend.API) error {
	api.AssertIsEqual(api.Mul(c.X, c.X, c.X), c.Y)
	return nil
}

// Synthetic correctness test only. Never creates/replaces production keys.
func TestCPUBackendAndWrongPublicInput(t *testing.T) {
	cs, e := frontend.Compile(ecc.BN254.ScalarField(), r1cs.NewBuilder, &cubic{})
	if e != nil {
		t.Fatal(e)
	}
	pk, vk, e := groth16.Setup(cs)
	if e != nil {
		t.Fatal(e)
	}
	w, e := frontend.NewWitness(&cubic{X: 3, Y: 27}, ecc.BN254.ScalarField())
	if e != nil {
		t.Fatal(e)
	}
	b, _ := selectBackend("cpu")
	proof, e := b.Prove(cs, pk, w)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := w.Public()
	if e != nil {
		t.Fatal(e)
	}
	if e = groth16.Verify(proof, vk, pub); e != nil {
		t.Fatal(e)
	}
	wrong, e := frontend.NewWitness(&cubic{Y: 28}, ecc.BN254.ScalarField(), frontend.PublicOnly())
	if e != nil {
		t.Fatal(e)
	}
	if e = groth16.Verify(proof, vk, wrong); e == nil {
		t.Fatal("wrong public input verified")
	}
}
