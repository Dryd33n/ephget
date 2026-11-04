package schedule

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Registers a real task under a test-only name, pointed at a program that doesn't
// exist, and deletes it afterwards.
func TestEnsureRegistersAndRepairs(t *testing.T) {
	name := fmt.Sprintf("ephget-reap-test-%d", os.Getpid())
	t.Cleanup(func() { Delete(name) })
	first := `C:\no such folder\R&D\ephgetw.exe`
	if err := Ensure(name, first); err != nil {
		t.Fatal(err)
	}
	if got, err := Target(name); err != nil || got != first {
		t.Fatalf("target %q, %v", got, err)
	}
	if err := Ensure(name, strings.ToUpper(first)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Target(name); got != first {
		t.Fatalf("re-registered for a target differing only in case: %q", got)
	}
	second := `C:\elsewhere\ephgetw.exe`
	if err := Ensure(name, second); err != nil {
		t.Fatal(err)
	}
	if got, _ := Target(name); got != second {
		t.Fatalf("not repaired: %q", got)
	}

	out, err := exec.Command("schtasks", "/query", "/xml", "/tn", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<LogonTrigger>", "<Interval>PT1H</Interval>", "<StartWhenAvailable>true",
		"<LogonType>InteractiveToken", "<Arguments>reap</Arguments>"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("task is missing %s:\n%s", want, out)
		}
	}
	// Windows leaves out settings that match its defaults, so these show as absent.
	for _, unwanted := range []string{"<WakeToRun>true", "HighestAvailable"} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("task has %s:\n%s", unwanted, out)
		}
	}
}

func TestMissingTaskIsAnError(t *testing.T) {
	if _, err := Target("ephget-reap-test-does-not-exist"); err == nil {
		t.Fatal("found a task that doesn't exist")
	}
}
