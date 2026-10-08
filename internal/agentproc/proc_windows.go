//go:build windows

package agentproc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// newLookup takes ONE process snapshot and answers every step of a walk from it,
// rather than re-snapshotting the whole table per ancestor. The start time comes
// from the live process, because a snapshot entry carries none.
func newLookup() (func(int) (procInfo, error), error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // a read-only snapshot handle; a close failure leaves nothing to undo

	entries := make(map[int]windows.ProcessEntry32)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		entries[int(e.ProcessID)] = e
	}
	return func(pid int) (procInfo, error) {
		entry, ok := entries[pid]
		if !ok {
			return procInfo{}, errNoProcess
		}
		start, err := creationTime(pid)
		if err != nil {
			return procInfo{}, err
		}
		return procInfo{
			ppid:  int(entry.ParentProcessID),
			name:  windows.UTF16ToString(entry.ExeFile[:]),
			start: start,
		}, nil
	}, nil
}

// creationTime reads a live process's creation time as a FILETIME count.
func creationTime(pid int) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h) //nolint:errcheck // a query-only handle; a close failure leaves nothing to undo
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	start := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if start == 0 {
		return 0, errNoStart
	}
	return start, nil
}
