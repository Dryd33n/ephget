package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTryAcquireAndWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("TryAcquire while held = %v", err)
	}
	got := make(chan error)
	go func() {
		l, err := Acquire(path)
		if err == nil {
			l.Release()
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("Acquire returned while the lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	first.Release()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}
