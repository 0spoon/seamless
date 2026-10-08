//go:build darwin

package agentproc

import (
	"golang.org/x/sys/unix"
)

// newLookup reads the process table through sysctl(kern.proc.pid), the call ps
// itself makes. It needs no privilege for another user's process, and no cgo.
func newLookup() (func(int) (procInfo, error), error) {
	return lookupDarwin, nil
}

func lookupDarwin(pid int) (procInfo, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return procInfo{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return procInfo{}, errNoProcess
	}
	tv := kp.Proc.P_starttime
	if tv.Sec <= 0 {
		return procInfo{}, errNoStart
	}
	// Microseconds since the epoch: the precision the kernel keeps, and enough to
	// tell two processes that ever held the same pid apart.
	start := uint64(tv.Sec)*1_000_000 + uint64(tv.Usec)
	return procInfo{
		ppid:  int(kp.Eproc.Ppid),
		name:  unix.ByteSliceToString(kp.Proc.P_comm[:]),
		start: start,
	}, nil
}
