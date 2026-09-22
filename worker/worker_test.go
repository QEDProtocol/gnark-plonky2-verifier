package worker

import (
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/zilong-dai/gnark/backend/groth16"
	csbn254 "github.com/zilong-dai/gnark/constraint/bn254"
	"github.com/zilong-dai/gnark/constraint/solver"
	"github.com/zilong-dai/gnark/frontend"
	"github.com/zilong-dai/gnark/frontend/cs/r1cs"
)

var solveHintCalls atomic.Int64
var solveHintFails atomic.Bool

func countedIdentityHint(_ *big.Int, inputs, outputs []*big.Int) error {
	solveHintCalls.Add(1)
	if solveHintFails.Load() {
		return errors.New("test hint failure")
	}
	outputs[0].Set(inputs[0])
	return nil
}

type singleSolveCircuit struct {
	X frontend.Variable
	Y frontend.Variable `gnark:",public"`
}

func (c *singleSolveCircuit) Define(api frontend.API) error {
	out, err := api.Compiler().NewHint(countedIdentityHint, 1, c.X)
	if err != nil {
		return err
	}
	api.AssertIsEqual(out[0], c.X)
	api.AssertIsEqual(api.Mul(out[0], out[0]), c.Y)
	return nil
}

func TestProveWithDiagnosticsSingleSolve(t *testing.T) {
	// Hint execution counts actual solver passes, including any diagnostic retry.
	// These subtests intentionally run serially because they share the hint counters.
	solver.RegisterHint(countedIdentityHint)
	ccs, err := frontend.Compile(ecc.BN254.ScalarField(), r1cs.NewBuilder, &singleSolveCircuit{})
	if err != nil {
		t.Fatal(err)
	}
	pk, vk, err := groth16.Setup(ccs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		y           int
		hintFailure bool
	}{
		{name: "valid", y: 9},
		{name: "unsatisfied_constraint", y: 10},
		{name: "hint_failure", y: 9, hintFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			solveHintCalls.Store(0)
			solveHintFails.Store(tc.hintFailure)
			t.Cleanup(func() { solveHintFails.Store(false) })
			wit, err := frontend.NewWitness(&singleSolveCircuit{X: 3, Y: tc.y}, ecc.BN254.ScalarField())
			if err != nil {
				t.Fatal(err)
			}
			proof, err := proveWithDiagnostics(ccs, pk, wit)
			if calls := solveHintCalls.Load(); calls != 1 {
				t.Fatalf("expected one solver pass, got %d", calls)
			}
			if tc.hintFailure || tc.y != 9 {
				if err == nil || proof != nil {
					t.Fatal("invalid witness must return an error and no proof")
				}
				if tc.hintFailure {
					if !strings.Contains(err.Error(), "test hint failure") {
						t.Fatalf("hint error was not preserved: %v", err)
					}
				} else {
					var constraintError *csbn254.UnsatisfiedConstraintError
					if !errors.As(err, &constraintError) {
						t.Fatalf("constraint error was not preserved: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			public, err := wit.Public()
			if err != nil {
				t.Fatal(err)
			}
			if err := groth16.Verify(proof, vk, public); err != nil {
				t.Fatalf("valid proof rejected: %v", err)
			}
			wrongPublic, err := frontend.NewWitness(&singleSolveCircuit{Y: 10}, ecc.BN254.ScalarField(), frontend.PublicOnly())
			if err != nil {
				t.Fatal(err)
			}
			if err := groth16.Verify(proof, vk, wrongPublic); err == nil {
				t.Fatal("proof accepted with different public inputs")
			}
		})
	}
}
