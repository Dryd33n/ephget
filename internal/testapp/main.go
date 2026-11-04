// Command testapp stands in for a downloaded app in tests, so that no real app
// runs on a development machine. Each process appends "start ..." and "end ..."
// lines to the file named by TESTAPP_LOG.
//
// TESTAPP_MODE selects its behaviour:
//
//	(empty)   run for TESTAPP_MILLIS and exit
//	handoff   start a copy of itself, which starts one more, and exit straight away
//	console   record whether its standard handles reach a real console, then exit
//
// TESTAPP_SETTINGS names a folder to create, as an app's settings would be.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func log(event string) {
	f, err := os.OpenFile(os.Getenv("TESTAPP_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return
	}
	defer f.Close()
	cwd, _ := os.Getwd()
	fmt.Fprintf(f, "%s %d %s %s\n", event, os.Getpid(), filepath.Base(cwd), time.Now().Format(time.RFC3339Nano))
}

func isConsole(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func main() {
	log("start")
	defer log("end")
	if settings := os.Getenv("TESTAPP_SETTINGS"); settings != "" {
		os.MkdirAll(settings, 0o755)
	}
	mode := os.Getenv("TESTAPP_MODE")
	if mode == "console" {
		log(fmt.Sprintf("console stdin=%t stdout=%t stderr=%t args=%q",
			isConsole(os.Stdin), isConsole(os.Stdout), isConsole(os.Stderr), os.Args[1:]))
		return
	}
	if mode == "handoff" {
		remaining := 2
		if len(os.Args) > 1 {
			remaining, _ = strconv.Atoi(os.Args[1])
		}
		if remaining > 0 {
			self, _ := os.Executable()
			child := exec.Command(self, strconv.Itoa(remaining-1))
			if err := child.Start(); err != nil {
				log("spawn-failed")
			}
			return
		}
	}
	millis, _ := strconv.Atoi(os.Getenv("TESTAPP_MILLIS"))
	time.Sleep(time.Duration(millis) * time.Millisecond)
}
