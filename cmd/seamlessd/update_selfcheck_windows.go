//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procIsProcessInJob is kernel32's IsProcessInJob, which x/sys/windows does not
// wrap.
var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// ownProcess reads this process's job for outsideServiceCheck: whether it is
// in one, and that job's limit flags. A NULL job handle asks about the
// process's own job, its innermost when jobs nest.
func ownProcess() (selfProcess, error) {
	p := selfProcess{pid: os.Getpid()}
	if err := procIsProcessInJob.Find(); err != nil {
		return p, err
	}
	var in int32
	if r, _, err := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in))); r == 0 {
		return p, fmt.Errorf("IsProcessInJob: %w", err)
	}
	if in == 0 {
		return p, nil
	}
	p.inJob = true
	var ext windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&ext)), uint32(unsafe.Sizeof(ext)), nil); err != nil {
		return p, fmt.Errorf("read the job's limits: %w", err)
	}
	p.jobLimits = ext.BasicLimitInformation.LimitFlags
	return p, nil
}

// parentImageName names this process's parent's executable, without its
// directory, from one process snapshot: no access to the parent is needed,
// which a standard user does not have to the Task Scheduler's svchost.exe.
func parentImageName() (string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "", fmt.Errorf("snapshot the process table: %w", err)
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // a read-only snapshot handle; a close failure leaves nothing to undo

	self := uint32(os.Getpid())
	names := map[uint32]string{}
	parent, found := uint32(0), false
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		names[e.ProcessID] = windows.UTF16ToString(e.ExeFile[:])
		if e.ProcessID == self {
			parent, found = e.ParentProcessID, true
		}
	}
	if !found {
		return "", errors.New("this process is not in the process table")
	}
	name, ok := names[parent]
	if !ok {
		return "", fmt.Errorf("the parent process %d has exited", parent)
	}
	return name, nil
}

// isCurrentUser reports whether account -- a Scheduled Task principal's
// UserId, which Task Scheduler gives as a SID, DOMAIN\name or a bare name --
// is the user this process runs as, compared by SID.
func isCurrentUser(account string) (bool, error) {
	me, err := tokenUserSID()
	if err != nil {
		return false, err
	}
	sid, err := windows.StringToSid(account)
	if err != nil {
		if sid, _, _, err = windows.LookupSID("", account); err != nil {
			return false, fmt.Errorf("look up %s: %w", account, err)
		}
	}
	return sid.Equals(me), nil
}

// currentUserSID is the SID this process runs as, in S-1-... form.
func currentUserSID() (string, error) {
	sid, err := tokenUserSID()
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

// tokenUserSID reads this process's user from its token, through the
// pseudo-handle that needs no opening or closing.
func tokenUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the process token's user: %w", err)
	}
	return user.User.Sid.Copy()
}
