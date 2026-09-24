//go:build proverbench && msmhook && iciclemsm && !cgo

package main

import "errors"

func newICICLEEngine(backendOptions) (msmEngine, error) {
	return nil, errors.New("icicle_requires_cgo")
}
