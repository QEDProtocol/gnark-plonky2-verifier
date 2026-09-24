//go:build proverbench && msmhook && !iciclemsm

package main

import "errors"

func newICICLEEngine(backendOptions) (msmEngine, error) {
	return nil, errors.New("icicle_msm_not_built")
}
