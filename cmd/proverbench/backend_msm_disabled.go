//go:build proverbench && !msmhook

package main

import "errors"

func newMSMBackend(string, backendOptions) (proverBackend, error) {
	return nil, errors.New("msm_hook_not_built")
}
