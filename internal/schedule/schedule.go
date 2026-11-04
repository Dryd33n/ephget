// Package schedule registers the per-user task that runs the reaper. It runs
// schtasks.exe, so only ephget.exe uses it: ephgetw has no need to, and starting a
// console program from a GUI program would flash a window.
package schedule

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// TaskName is the real task. Tests use their own name.
const TaskName = "ephget-reap"

var commandTag = regexp.MustCompile(`<Command>([^<]*)</Command>`)

// Ensure registers the task unless it already runs target, which also repairs a
// task left pointing at an old location.
func Ensure(name, target string) error {
	if current, err := Target(name); err == nil && strings.EqualFold(current, target) {
		return nil
	}
	return Register(name, target)
}

// Target returns the program the task runs.
func Target(name string) (string, error) {
	out, err := exec.Command("schtasks", "/query", "/xml", "/tn", name).Output()
	if err != nil {
		return "", fmt.Errorf("query task %s: %w", name, err)
	}
	match := commandTag.FindSubmatch(out)
	if match == nil {
		return "", fmt.Errorf("task %s has no command", name)
	}
	return html.UnescapeString(string(match[1])), nil
}

// Register creates the task, replacing any task with that name.
func Register(name, target string) error {
	account, err := user.Current()
	if err != nil {
		return err
	}
	definition, err := taskXML(account.Username, target, time.Now())
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "ephget-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(utf16LE(definition))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	out, err := exec.Command("schtasks", "/create", "/f", "/tn", name, "/xml", file.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("register task %s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}

// Delete removes the task.
func Delete(name string) error {
	out, err := exec.Command("schtasks", "/delete", "/f", "/tn", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete task %s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}

func escape(s string) (string, error) {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// taskXML describes a task that runs at logon and hourly, soon after any start it
// missed, on battery too, and never wakes the PC. InteractiveToken means it only
// runs while the user is logged on, and needs no stored password or admin rights.
func taskXML(username, target string, now time.Time) (string, error) {
	account, err := escape(username)
	if err != nil {
		return "", err
	}
	command, err := escape(target)
	if err != nil {
		return "", err
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Deletes ephget apps that have gone unused past their time limit.</Description></RegistrationInfo>
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>` + account + `</UserId></LogonTrigger>
    <TimeTrigger>
      <Repetition><Interval>PT1H</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>
      <StartBoundary>` + now.Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author"><UserId>` + account + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + command + `</Command><Arguments>reap</Arguments></Exec></Actions>
</Task>
`, nil
}

// utf16LE is the encoding schtasks reads reliably, whatever the system's code page.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(units))
	binary.LittleEndian.PutUint16(out, 0xFEFF)
	for _, unit := range units {
		out = binary.LittleEndian.AppendUint16(out, unit)
	}
	return out
}
