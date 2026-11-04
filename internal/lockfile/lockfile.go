// Package lockfile provides whole-file exclusive locks that Windows releases
// when the holder exits, so a killed process can't leave an app locked forever.
package lockfile

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
	errorIOPending          = syscall.Errno(997)
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

// ErrHeld is returned by TryAcquire when another holder has the lock.
var ErrHeld = errors.New("lock is held")

// Lock is a held lock.
type Lock struct{ file *os.File }

// Acquire waits until the lock at path is free.
func Acquire(path string) (*Lock, error) { return lock(path, lockfileExclusiveLock) }

// TryAcquire returns ErrHeld rather than waiting.
func TryAcquire(path string) (*Lock, error) {
	return lock(path, lockfileExclusiveLock|lockfileFailImmediately)
}

func lock(path string, flags uintptr) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	ok, _, callErr := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		f.Close()
		if callErr == errorLockViolation || callErr == errorIOPending {
			return nil, ErrHeld
		}
		return nil, callErr
	}
	return &Lock{file: f}, nil
}

// Release frees the lock. The file stays, so a waiter never locks a deleted one.
func (l *Lock) Release() { l.file.Close() }
