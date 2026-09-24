//go:build proverbench

package worker

// BenchmarkAssignment mirrors GenerateProof's input preparation at the pinned
// production commit. It deliberately skips key creation, logging and retries.
// Only compiled for the offline lab; production entry points are unchanged.
import (
	"encoding/binary"
	"github.com/cf/gnark-plonky2-verifier/types"
	"github.com/cf/gnark-plonky2-verifier/variables"
	"github.com/zilong-dai/gnark/frontend"
	gosha3 "golang.org/x/crypto/sha3"
	"math/big"
)

func BenchmarkAssignment(common_circuit_data, proof_with_public_inputs, verifier_only_circuit_data string) *CRVerifierCircuit {
	commonCircuitData := types.ReadCommonCircuitDataRaw(common_circuit_data)
	verifierOnlyCircuitDataRaw := types.ReadVerifierOnlyCircuitDataRaw(verifier_only_circuit_data)
	verifierOnlyCircuitData := variables.DeserializeVerifierOnlyCircuitData(verifierOnlyCircuitDataRaw)

	rawProofWithPis := types.ReadProofWithPublicInputsRaw(proof_with_public_inputs)
	proofWithPis := variables.DeserializeProofWithPublicInputs(rawProofWithPis)

	// Pack LE bits (grouped as Goldilocks 64-bit limbs) back into big-endian bytes.
	if len(rawProofWithPis.PublicInputs) == 0 || len(rawProofWithPis.PublicInputs)%64 != 0 {
		panic("invalid original public inputs, expected a non-empty multiple of 64 LE bits")
	}
	limbCount := len(rawProofWithPis.PublicInputs) / 64
	buf := make([]byte, limbCount*8)
	for i := 0; i < limbCount; i++ {
		var val uint64
		for j := 0; j < 64; j++ {
			if rawProofWithPis.PublicInputs[i*64+j] == 1 {
				val |= 1 << uint(j)
			}
		}
		binary.BigEndian.PutUint64(buf[i*8:], val)
	}
	// Compute keccak256
	h := gosha3.NewLegacyKeccak256()
	h.Write(buf)
	hashBytes := h.Sum(nil)

	// Split into hi (128 bits) and lo (128 bits)
	hi := new(big.Int).SetBytes(hashBytes[:16])
	lo := new(big.Int).SetBytes(hashBytes[16:])
	hiVar := frontend.Variable(hi)
	loVar := frontend.Variable(lo)

	circuit := CRVerifierCircuit{
		PublicInputs:            make([]frontend.Variable, 2),
		Proof:                   proofWithPis.Proof,
		OriginalPublicInputs:    proofWithPis.PublicInputs,
		VerifierOnlyCircuitData: verifierOnlyCircuitData,
		CommonCircuitData:       commonCircuitData,
	}

	assignment := CRVerifierCircuit{
		PublicInputs:            []frontend.Variable{hiVar, loVar},
		Proof:                   circuit.Proof,
		OriginalPublicInputs:    circuit.OriginalPublicInputs,
		VerifierOnlyCircuitData: circuit.VerifierOnlyCircuitData,
		CommonCircuitData:       commonCircuitData,
	}
	return &assignment
}
