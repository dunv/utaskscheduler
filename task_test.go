package utaskscheduler_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	uts "github.com/dunv/utaskscheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSimple(t *testing.T) {
	done := make(chan bool)
	task, err := uts.NewTask(
		uts.WithTimeout(time.Second),
		uts.WithReturnChannel(done),
		uts.WithPrintStartAndEndInOutput(),
		uts.WithShell("/bin/sh", "-c", "echo hallo"),
	)
	require.NoError(t, err)
	task.Run()
	<-done
	output := task.Output()
	assert.Len(t, output, 3)
	assert.Equal(t, "hallo", output[1].Output)
}

func TestRunInitError(t *testing.T) {
	done := make(chan bool)
	task, err := uts.NewTask(
		uts.WithTimeout(time.Second),
		uts.WithReturnChannel(done),
		uts.WithShell("/bin/shNotExisting", "-c", "echo hallo"),
	)
	require.NoError(t, err)
	task.Run()
	<-done
	output := task.Output()
	assert.Len(t, output, 1)
	assert.Equal(t, "Error starting (fork/exec /bin/shNotExisting: no such file or directory)", output[0].Output)
}

func TestShellTaskSuccessInTime(t *testing.T) {
	success := runShell(t, "/bin/sh", []string{"-c", "echo hello"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func TestShellTaskSuccessNotInTime(t *testing.T) {
	success := runShell(t, "/bin/sh", []string{"-c", "echo 1 && sleep 1 && echo 2 && sleep 1 && echo 3"}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskError(t *testing.T) {
	success := runShell(t, "/bin/sh", []string{"-c", "exit 1"}, 2*time.Second)
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

	success := runShell(t, "go", []string{"build", "-o", executablePath}, 10*time.Second, workingDir)
	if !success {
		t.Error("Task exited with error, but should have not")
	}

	success = runShell(t, "sh", []string{"-c", executablePath}, 2*time.Second)
	if success {
		t.Error("Task exited successfully, but should have not")
	}
}

func TestShellTaskEndlessWithDetachedChildren(t *testing.T) {
	success := runShell(t, "sh", []string{"-c", "sleep 1000 &"}, 2*time.Second)
	if !success {
		t.Error("Task exited with error, but should have not")
	}
}

func TestFunctionSuccess(t *testing.T) {
	success := runFunction(t, func(ctx context.Context, taskOutputChannel chan string) int {
		taskOutputChannel <- "TestOutputBeforeSuccess"
		time.Sleep(time.Second)
		return 0
	}, 2*time.Second)
	if !success {
		t.Error("Function exited with error, but should have not")
	}
}

func TestFunctionError(t *testing.T) {
	success := runFunction(t, func(ctx context.Context, taskOutputChannel chan string) int {
		taskOutputChannel <- "TestOutputBeforeError"
		time.Sleep(200 * time.Millisecond)
		return -1
	}, 2*time.Second)
	if success {
		t.Error("Function exited with error, but should have not")
	}
}

func TestFunctionTimeout(t *testing.T) {
	success := runFunction(t, func(ctx context.Context, taskOutputChannel chan string) int {
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
	done := make(chan bool)
	progressChannel, outputChannel := setupTaskProgress()

	task, err := uts.NewTask(
		uts.WithTimeout(5*time.Second),
		uts.WithOutputChannel(outputChannel),
		uts.WithProgressChannel(progressChannel),
		uts.WithReturnChannel(done),
		uts.WithShell("/bin/sh", "-c", "sleep 4"),
	)
	require.NoError(t, err)

	go task.Run()

	time.Sleep(1 * time.Second)
	task.Cancel()

	success := <-done
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)

	if success {
		t.Error("Task exited with success, but should have not")
	}
}

func TestFunctionTaskCancel(t *testing.T) {
	returnChannel := make(chan bool)
	progressChannel, outputChannel := setupTaskProgress()
	task, err := uts.NewTask(
		uts.WithTimeout(3*time.Second),
		uts.WithOutputChannel(outputChannel),
		uts.WithProgressChannel(progressChannel),
		uts.WithReturnChannel(returnChannel),
		uts.WithFunction(func(ctx context.Context, outputChannel chan string) int {
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
		}),
	)
	require.NoError(t, err)
	go task.Run()

	time.Sleep(1 * time.Second)
	task.Cancel()

	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)

	if success {
		t.Error("Task exited with success, but should have not")
	}
}

func runShell(t *testing.T, cmd string, args []string, timeout time.Duration, workingDir ...string) bool {
	returnChannel := make(chan bool)
	progressChannel, outputChannel := setupTaskProgress()
	opts := []uts.TaskOption{
		uts.WithTimeout(timeout),
		uts.WithShell(cmd, args...),
		uts.WithOutputChannel(outputChannel),
		uts.WithProgressChannel(progressChannel),
		uts.WithReturnChannel(returnChannel),
	}
	if len(workingDir) == 1 {
		opts = append(opts, uts.WithShellWorkingDir(workingDir[0]))
	}
	task, err := uts.NewTask(opts...)
	require.NoError(t, err)

	// Make sure that locking in the String() function does not deadlock anything
	go func(task uts.Task) {
		for {
			_ = task.String()
			time.Sleep(time.Millisecond)
		}
	}(task)

	task.Run()
	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)
	return success
}

func runFunction(t *testing.T, function func(ctx context.Context, outputChannel chan string) int, timeout time.Duration) bool {
	returnChannel := make(chan bool)
	progressChannel, outputChannel := setupTaskProgress()
	task, err := uts.NewTask(
		uts.WithTimeout(timeout),
		uts.WithFunction(function),
		uts.WithOutputChannel(outputChannel),
		uts.WithProgressChannel(progressChannel),
		uts.WithReturnChannel(returnChannel),
	)
	require.NoError(t, err)

	// Make sure that locking in the String() function does not deadlock anything
	go func(task uts.Task) {
		for {
			_ = task.String()
			time.Sleep(time.Millisecond)
		}
	}(task)

	task.Run()
	success := <-returnChannel
	// Add this so output is not fragmented by go-testing output
	time.Sleep(500 * time.Millisecond)
	return success
}

func setupTaskProgress() (chan uts.TaskStatusUpdate, chan uts.TaskOutput) {
	progressChannel := make(chan uts.TaskStatusUpdate)
	outputChannel := make(chan uts.TaskOutput)
	logger := uts.NewDefaultLogger()

	go func(outputChannel chan uts.TaskOutput) {
		for output := range outputChannel {
			logger.Infof("%v", output)
		}
	}(outputChannel)

	go func(progressChannel chan uts.TaskStatusUpdate) {
		for progress := range progressChannel {
			logger.Infof("%v", progress)
		}
	}(progressChannel)

	return progressChannel, outputChannel
}
