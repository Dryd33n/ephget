package launch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	processQueryLimited = 0x1000
	synchronize         = 0x00100000
	waitObject0         = 0
	waitFailed          = 0xFFFFFFFF
	infinite            = 0xFFFFFFFF
	maxWaitObjects      = 64
	blindPollMillis     = 5000

	errorInvalidParameter = syscall.Errno(87)
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procWaitForMultipleObjects     = kernel32.NewProc("WaitForMultipleObjects")
)

// WaitForFolder returns once no process running an image inside dir remains.
//
// It blocks on process handles, so it uses no CPU while waiting. A child that
// outlives its parent is still found: it exists before its parent exits, and the
// parent's exit wakes this loop to look again.
func WaitForFolder(dir string) error {
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(canon)
	if err != nil {
		return err
	}
	s := scanner{
		prefix:   strings.ToLower(abs) + `\`,
		deadSeen: map[uint32]bool{},
		image:    make([]uint16, 32768),
	}
	for {
		result, err := s.scan()
		if err != nil {
			return err
		}
		if len(result.handles) == 0 && !result.blind {
			if !result.diedDuringScan {
				return nil
			}
			continue
		}
		err = waitForAny(result.handles, result.blind)
		for _, h := range result.handles {
			syscall.CloseHandle(h)
		}
		if err != nil {
			return err
		}
	}
}

type scanResult struct {
	handles []syscall.Handle
	// blind means a process exists that can be seen but not waited on, such as an
	// elevated one, and so has to be checked for on a timer.
	blind bool
	// diedDuringScan means a process in the snapshot was gone by the time it was
	// examined. It may have started a child after the snapshot was taken, so an
	// otherwise empty result proves nothing and the scan must run again.
	diedDuringScan bool
}

type scanner struct {
	prefix string
	// deadSeen stops a dead process, kept listed by someone else's open handle,
	// from forcing a fresh scan every time.
	deadSeen map[uint32]bool
	image    []uint16
}

func (s *scanner) scan() (scanResult, error) {
	var r scanResult
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return r, fmt.Errorf("list processes: %w", err)
	}
	defer syscall.CloseHandle(snapshot)

	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	for more := syscall.Process32First(snapshot, &entry); more == nil; more = syscall.Process32Next(snapshot, &entry) {
		pid := entry.ProcessID
		h, err := syscall.OpenProcess(processQueryLimited|synchronize, false, pid)
		if err == nil {
			switch {
			case !s.inFolder(h):
				syscall.CloseHandle(h)
			case exited(h):
				syscall.CloseHandle(h)
				if !s.deadSeen[pid] {
					s.deadSeen[pid] = true
					r.diedDuringScan = true
				}
			default:
				r.handles = append(r.handles, h)
			}
			continue
		}
		if errors.Is(err, errorInvalidParameter) {
			// This PID no longer exists, and its path can no longer be read. The
			// Idle process (PID 0) also answers this way, which is why this counts
			// only once per PID.
			if !s.deadSeen[pid] {
				s.deadSeen[pid] = true
				r.diedDuringScan = true
			}
			continue
		}
		h, err = syscall.OpenProcess(processQueryLimited, false, pid)
		if err != nil {
			continue // a system or other user's process, which can't be this app's
		}
		if s.inFolder(h) {
			r.blind = true
		}
		syscall.CloseHandle(h)
	}
	return r, nil
}

func (s *scanner) inFolder(h syscall.Handle) bool {
	size := uint32(len(s.image))
	ok, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&s.image[0])), uintptr(unsafe.Pointer(&size)))
	return ok != 0 && strings.HasPrefix(strings.ToLower(syscall.UTF16ToString(s.image[:size])), s.prefix)
}

// exited catches a process that has died but is still listed. Waiting on it would
// return at once, and the loop would spin until it disappeared.
func exited(h syscall.Handle) bool {
	result, err := syscall.WaitForSingleObject(h, 0)
	return err == nil && result == waitObject0
}

// waitForAny returns when any handle is signalled. Any exit is worth a fresh look,
// so the limit on how many handles one wait can take doesn't matter.
func waitForAny(handles []syscall.Handle, blind bool) error {
	timeout := uintptr(infinite)
	if blind {
		timeout = blindPollMillis
	}
	if len(handles) == 0 {
		time.Sleep(blindPollMillis * time.Millisecond)
		return nil
	}
	count := min(len(handles), maxWaitObjects)
	result, _, err := procWaitForMultipleObjects.Call(uintptr(count), uintptr(unsafe.Pointer(&handles[0])), 0, timeout)
	if uint32(result) == waitFailed {
		return fmt.Errorf("wait for app: %w", err)
	}
	return nil
}
