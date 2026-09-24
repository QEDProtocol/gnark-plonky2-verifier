//go:build proverbench

package main

import (
	"errors"
	"os"

	"github.com/zilong-dai/gnark/backend/groth16"
	native "github.com/zilong-dai/gnark/backend/groth16/bn254"
	"github.com/zilong-dai/gnark/backend/witness"
	"github.com/zilong-dai/gnark/constraint"
)

const (
	defaultMSMChunkSize      = 1 << 20
	defaultMSMInternalChunks = 4
)

type backendOptions struct {
	Device, ChunkSize, InternalChunks int
	BackendDir                        string
	Recorder                          *recorder
}
type proverBackend interface {
	Name() string
	LoadPK(string) (groth16.ProvingKey, error)
	Prove(constraint.ConstraintSystem, groth16.ProvingKey, witness.Witness) (groth16.Proof, error)
	Close()
}
type cpuBackend struct{}

func (cpuBackend) Name() string { return "cpu" }
func (cpuBackend) Close()       {}
func (cpuBackend) LoadPK(path string) (groth16.ProvingKey, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	pk := new(native.ProvingKey)
	_, e = pk.ReadFrom(f)
	if e != nil {
		return nil, e
	}
	return pk, nil
}
func (cpuBackend) Prove(c constraint.ConstraintSystem, p groth16.ProvingKey, w witness.Witness) (groth16.Proof, error) {
	return groth16.Prove(c, p, w)
}
func selectBackend(name string) (proverBackend, error) {
	return selectBackendWithOptions(name, backendOptions{ChunkSize: defaultMSMChunkSize, InternalChunks: defaultMSMInternalChunks})
}
func selectBackendWithOptions(name string, opt backendOptions) (proverBackend, error) {
	if opt.Device < 0 || opt.ChunkSize < 1 || opt.ChunkSize > 1<<20 {
		return nil, errors.New("invalid_backend_options")
	}
	switch name {
	case "cpu":
		return cpuBackend{}, nil
	case "cpu-msm", "icicle-msm":
		return newMSMBackend(name, opt)
	default:
		return nil, errors.New("backend_not_implemented")
	}
}
