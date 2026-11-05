package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildTestApp(t *testing.T) (exe, logFile string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "testapp.exe")
	out, err := exec.Command("go", "build", "-ldflags", "-H=windowsgui", "-o", exe, "github.com/Ephget/ephget/internal/testapp").CombinedOutput()
	if err != nil {
		t.Fatalf("build testapp: %v\n%s", err, out)
	}
	return exe, filepath.Join(t.TempDir(), "log.txt")
}

func TestWaitForFolderWithNothingRunning(t *testing.T) {
	start := time.Now()
	if err := WaitForFolder(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("took too long")
	}
}

func TestWaitForFolderMissing(t *testing.T) {
	if err := WaitForFolder(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("no error")
	}
}

func TestWaitCoversAChainOfHandoffs(t *testing.T) {
	exe, logFile := buildTestApp(t)
	t.Setenv("TESTAPP_LOG", logFile)
	t.Setenv("TESTAPP_MODE", "handoff")
	t.Setenv("TESTAPP_MILLIS", "200")
	// The race this guards against depends on timing, so one pass proves little.
	for range 12 {
		os.Remove(logFile)
		h, err := startApp(exe)
		if err != nil {
			t.Fatal(err)
		}
		syscall.WaitForSingleObject(h, syscall.INFINITE)
		syscall.CloseHandle(h)
		if err := WaitForFolder(filepath.Dir(exe)); err != nil {
			t.Fatal(err)
		}
		// Three processes: the first two hand off and exit, the last one runs on.
		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		if ends := strings.Count(string(data), "end "); ends != 3 {
			t.Fatalf("WaitForFolder returned after %d of 3 processes ended:\n%s", ends, data)
		}
	}
}

func TestWaitIgnoresOtherFolders(t *testing.T) {
	exe, logFile := buildTestApp(t)
	t.Setenv("TESTAPP_LOG", logFile)
	t.Setenv("TESTAPP_MILLIS", "20000")
	cmd := exec.Command(exe)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	// A sibling folder sharing a name prefix must not count as inside.
	start := time.Now()
	sibling := filepath.Dir(exe) + "2"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sibling)
	if err := WaitForFolder(sibling); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited on a process in another folder")
	}
}
