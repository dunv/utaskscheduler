package utaskscheduler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/ulog"
)

func TestShellTaskSuccessInTime(t *testing.T) {
	success := run("/bin/sh", []string{"-c", "echo hello"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func TestShellTaskSuccessNotInTime(t *testing.T) {
	success := run("/bin/sh", []string{"-c", "echo 1 && sleep 1 && echo 2 && sleep 1 && echo 3"}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskEndlessWithIgnoreExitSignal(t *testing.T) {
	workingDir, err := filepath.Abs("ignoreAllSignals")
	if err != nil {
		panic(err)
	}

	executablePath, err := filepath.Abs(filepath.Join("ignoreAllSignals", "ignoreAllSignals"))
	if err != nil {
		panic(err)
	}

	success := run("go", []string{"build", "-o", executablePath}, 10*time.Second, workingDir)
	if !success {
		t.Error("Task exited with error, but should have not")
	}

	success = run("sh", []string{"-c", executablePath}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskEndlessWithDetachedChildren(t *testing.T) {
	success := run("sh", []string{"-c", "sleep 1000 &"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func run(cmd string, args []string, timeout time.Duration, workingDir ...string) bool {
	progressChannel, outputChannel := setup()
	task := NewShellTask(cmd, args, uhelpers.PtrToDuration(timeout), outputChannel)
	if len(workingDir) == 1 {
		task.WorkingDir = uhelpers.PtrToString(workingDir[0])
	}
	returnChannel := make(chan bool)
	task.Run(progressChannel, &returnChannel)
	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)
	return success
}

func setup() (*chan Task, *chan TaskOutput) {
	progressChannel := make(chan Task)
	outputChannel := make(chan TaskOutput)

	go func(outputChannel chan TaskOutput) {
		for output := range outputChannel {
			ulog.Info(output)
		}
	}(outputChannel)

	go func(progressChannel chan Task) {
		for progress := range progressChannel {
			ulog.Info(progress)
		}
	}(progressChannel)

	return &progressChannel, &outputChannel
}
