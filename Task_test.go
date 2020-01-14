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

func TestShellTaskCancel(t *testing.T) {
	progressChannel, outputChannel := setupTaskProgress()
	// Start a task which needs 4 seconds to complete, timeout is longer, so it would complete in time
	task := NewShellTask("/bin/sh", []string{"-c", "sleep 4"}, uhelpers.PtrToDuration(5*time.Second), outputChannel)
	returnChannel := make(chan bool)
	go task.Run(progressChannel, &returnChannel)

	time.Sleep(1 * time.Second)
	task.Cancel()

	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)

	if success {
		t.Error("Task exited with success, but should have not")
	}
}

func TestFunctionTaskCancel(t *testing.T) {

	progressChannel, outputChannel := setupTaskProgress()
	task := NewFunctionTask(func(ctx context.Context, outputChannel chan string) int {
		start := time.Now()
		for {
			// output
			outputChannel <- "running"

			// if function gets 2 seconds to run: exit with success
			if time.Since(start) > 2*time.Second {
				return 0
			}

			// check for cancelled context "every 100ms"
			select {
			case <-ctx.Done():
				return -1
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
	}, uhelpers.PtrToDuration(3*time.Second), outputChannel)
	returnChannel := make(chan bool)
	go task.Run(progressChannel, &returnChannel)

	time.Sleep(1 * time.Second)
	task.Cancel()

	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)

	if success {
		t.Error("Task exited with success, but should have not")
	}
}

func runShell(cmd string, args []string, timeout time.Duration, workingDir ...string) bool {

	progressChannel, outputChannel := setupTaskProgress()
	task := NewShellTask(cmd, args, uhelpers.PtrToDuration(timeout), outputChannel)

	// Make sure that locking in the String() function does not deadlock anything
	go func(task *Task) {
		for {
			_ = task.String()
			time.Sleep(time.Millisecond)
		}
	}(task)

	if len(workingDir) == 1 {
		task.workingDir = uhelpers.PtrToString(workingDir[0])
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

	// Make sure that locking in the String() function does not deadlock anything
	go func(task *Task) {
		for {
			_ = task.String()
			time.Sleep(time.Millisecond)
		}
	}(task)

	returnChannel := make(chan bool)
	task.Run(progressChannel, &returnChannel)
	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)
	return success
}

func setupTaskProgress() (*chan *Task, *chan TaskOutput) {
	progressChannel := make(chan *Task)
	outputChannel := make(chan TaskOutput)

	go func(outputChannel chan TaskOutput) {
		for output := range outputChannel {
			ulog.Info(output)
		}
	}(outputChannel)

	go func(progressChannel chan *Task) {
		for progress := range progressChannel {
			ulog.Info(progress)
		}
	}(progressChannel)

	return &progressChannel, &outputChannel
}
