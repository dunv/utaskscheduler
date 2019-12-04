package utaskscheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/ulog"
)

func TestShellTaskSuccessInTime(t *testing.T) {
	success := runShell("/bin/sh", []string{"-c", "echo hello"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func TestShellTaskSuccessNotInTime(t *testing.T) {
	success := runShell("/bin/sh", []string{"-c", "echo 1 && sleep 1 && echo 2 && sleep 1 && echo 3"}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskError(t *testing.T) {
	success := runShell("/bin/sh", []string{"-c", "exit 1"}, 2*time.Second)
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

	success := runShell("go", []string{"build", "-o", executablePath}, 10*time.Second, workingDir)
	if !success {
		t.Error("Task exited with error, but should have not")
	}

	success = runShell("sh", []string{"-c", executablePath}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskEndlessWithDetachedChildren(t *testing.T) {
	success := runShell("sh", []string{"-c", "sleep 1000 &"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func TestFunctionSuccess(t *testing.T) {
	success := runFunction(func(ctx context.Context, taskOutputChannel chan string) int {
		taskOutputChannel <- "TestOutputBeforeSuccess"
		time.Sleep(time.Second)
		return 0
	}, 2*time.Second)
	if !success {
		t.Error("Function exited with error, but should have not")
	}
}

func TestFunctionError(t *testing.T) {
	success := runFunction(func(ctx context.Context, taskOutputChannel chan string) int {
		taskOutputChannel <- "TestOutputBeforeError"
		time.Sleep(200 * time.Millisecond)
		return -1
	}, 2*time.Second)
	if success {
		t.Error("Function exited with error, but should have not")
	}
}

func TestFunctionTimeout(t *testing.T) {
	success := runFunction(func(ctx context.Context, taskOutputChannel chan string) int {
		taskOutputChannel <- "TestOutputBeforeTimeout"
		select {
		case <-time.After(2 * time.Second):
			taskOutputChannel <- "TestOutputAfterTimeout (this should not be printed)"
		case <-ctx.Done():
			taskOutputChannel <- "Caught context timeout"
		}
		return 0
	}, 1*time.Second)
	if success {
		t.Error("Function exited with sucess, but should have not")
	}
}

func runShell(cmd string, args []string, timeout time.Duration, workingDir ...string) bool {
	progressChannel, outputChannel := setupTaskProgress()
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

func runFunction(function func(ctx context.Context, outputChannel chan string) int, timeout time.Duration) bool {
	progressChannel, outputChannel := setupTaskProgress()
	task := NewFunctionTask(function, uhelpers.PtrToDuration(timeout), outputChannel)
	returnChannel := make(chan bool)
	task.Run(progressChannel, &returnChannel)
	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)
	return success
}

func setupTaskProgress() (*chan Task, *chan TaskOutput) {
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
