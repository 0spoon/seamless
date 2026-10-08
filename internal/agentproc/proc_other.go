//go:build !darwin && !linux && !windows

package agentproc

import "errors"

// errUnsupported is every OS without a process-table reader here. Anchor then
// reports false, and the daemon resolves calls the way it always has.
var errUnsupported = errors.New("agentproc: unsupported platform")

func newLookup() (func(int) (procInfo, error), error) {
	return nil, errUnsupported
}
