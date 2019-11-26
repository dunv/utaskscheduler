package utaskscheduler

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/ulog"
	"github.com/google/uuid"
)

type Task struct {
	TaskGUID           uuid.UUID
	Type               TaskType
	Status             TaskStatus
	Command            string
	WorkingDir         *string
	Function           func(returnChannel *chan bool) (context.CancelFunc, []TaskOutput, int)
	Args               []string
	StartedAt          *time.Time
	FinishedAt         *time.Time
	Timeout            time.Duration
	Executed           bool
	Output             []TaskOutput
	OutputChannel      *chan TaskOutput
	ReturnChannel      *chan bool
	ProgressChannel    *chan Task
	ExitCode           int
	Error              error
	TaskMeta           interface{}
	PanicIfLostControl bool
}

func NewShellTask(
	command string,
	args []string,
	timeout *time.Duration,
	outputChannel *chan TaskOutput,
) *Task {
	usedTimeout := 10 * time.Second
	if timeout != nil {
		usedTimeout = *timeout
	}

	return &Task{
		TaskGUID:      uuid.New(),
		Type:          TASK_TYPE_SHELL,
		OutputChannel: outputChannel,
		Command:       command,
		Args:          args,
		Timeout:       usedTimeout,
	}
}

func (t Task) String() string {
	cmd := t.Command
	if len(cmd) > 20 {
		cmd = cmd[0:17] + "..."
	}
	return fmt.Sprintf("Task[type:%s timeout:%s command:%s status:%s]", t.Type, t.Timeout, cmd, t.Status)
}

func (t *Task) Run(progressChannel *chan Task, returnChannel *chan bool) context.CancelFunc {
	t.ReturnChannel = returnChannel
	t.ProgressChannel = progressChannel

	switch t.Type {
	case TASK_TYPE_SHELL:
		return t.runShell()
	default:
		return t.runFunction()
	}
}

func (t *Task) runFunction() context.CancelFunc {
	var cancelFunction context.CancelFunc
	// TODO: transport cancelFunc in a safe manner
	go func() {
		t.StartedAt = uhelpers.PtrToTime(time.Now())
		t.Status = TASK_STATUS_IN_PROGRESS
		t.sendProgressUpdate()
		cancelFunction, t.Output, t.ExitCode = t.Function(t.ReturnChannel)
		t.FinishedAt = uhelpers.PtrToTime(time.Now())

		t.Executed = true
		for _, output := range t.Output {
			if t.OutputChannel != nil {
				output.TaskGUID = t.TaskGUID
				output.TaskMeta = t.TaskMeta
				*t.OutputChannel <- output
			}
		}
		t.returnTask(true)
	}()
	return cancelFunction
}

func (t *Task) runShell() context.CancelFunc {
	// as exec.CommandContext predefinedly "only" sends cmd.Process.Kill(), we cannot use it here (https://golang.org/pkg/os/exec/#CommandContext)
	// we want the childProcess and all other descendants to be killed as well
	// https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
	cmd := exec.Command(t.Command, t.Args...)

	// Create our own context, so we can give a handle back which kills the process the way we want it to
	ctx, cancelFunc := context.WithCancel(context.Background())

	// use process-group-id as handle instead of process-id
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// set working dir only if set in task
	if t.WorkingDir != nil {
		cmd.Dir = *t.WorkingDir
	}

	// initial output (helps for debugging what is actually being run here)
	t.addOutput(TASK_OUTPUT_STDOUT, fmt.Sprintf("Running command '%s %s'", t.Command, strings.Join(t.Args, " ")))

	// initialize task and listeners
	t.StartedAt = uhelpers.PtrToTime(time.Now())
	t.Status = TASK_STATUS_IN_PROGRESS
	t.sendProgressUpdate()
	t.startConsumingOutputOfCommand(cmd)

	// actually start the task
	err := cmd.Start()

	// if the task could not be started -> set task attrs, publish err to output and return
	if err != nil {
		t.FinishedAt = uhelpers.PtrToTime(time.Now())
		t.Executed = true
		t.ExitCode = -1
		t.Error = err
		t.addOutput(TASK_OUTPUT_STDERR, fmt.Sprintf("Error starting (%s)", err.Error()))
		return cancelFunc
	}

	// So we can return here, the waiting for the task to be done processing is handled in a goRoutine
	processRegularExitChannel := make(chan bool)
	go func() {
		err := cmd.Wait()
		t.Executed = true
		t.FinishedAt = uhelpers.PtrToTime(time.Now())

		// Let our timeout-waiter know that we exited in time
		processRegularExitChannel <- true

		if err != nil {
			t.Error = err

			// Default to exit-code -1
			t.ExitCode = -1
			switch parsedErr := err.(type) {
			case *exec.ExitError:
				t.ExitCode = parsedErr.ExitCode()
			}

			t.addOutput(TASK_OUTPUT_STDERR, fmt.Sprintf("Error executing (%s)", err.Error()))
			t.returnTask(false)
		} else {
			t.ExitCode = 0

			t.addOutput(TASK_OUTPUT_STDOUT, "Done executing")
			t.returnTask(true)
		}
	}()

	// Implement our own timeout (see above for why)
	select {
	case <-ctx.Done():
		t.killProcessGroup(cmd)
	case <-time.After(t.Timeout):
		t.killProcessGroup(cmd)
	case <-processRegularExitChannel:
	}

	return cancelFunc
}

// Helper for processing output
func (t *Task) addOutput(outputType TaskOutputType, outputString string) {
	output := TaskOutput{
		TaskGUID: t.TaskGUID,
		TaskMeta: t.TaskMeta,
		Time:     time.Now(),
		Type:     TASK_OUTPUT_STDERR,
		Output:   outputString,
	}
	if t.OutputChannel != nil {
		*t.OutputChannel <- output
	}
	t.Output = append(t.Output, output)
}

// Helper for publishing into the returnChannel
func (t *Task) returnTask(success bool) {
	if t.ReturnChannel != nil {
		*t.ReturnChannel <- false
	}
}

// Helper for publishing into the progressUpdateChannel
func (t *Task) sendProgressUpdate() {
	if t.ProgressChannel != nil {
		*t.ProgressChannel <- *t
	}
}

// Helper for consuming stdErr and stdOut streams of a command
func (t *Task) startConsumingOutputOfCommand(cmd *exec.Cmd) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ulog.Errorf("Could not create stdout pipe for task (%s)", err)
	}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			t.addOutput(TASK_OUTPUT_STDOUT, strings.TrimSuffix(line, "\n"))
		}
	}()

	stderr, err := cmd.StderrPipe()
	if err != nil {
		ulog.Errorf("Could not create stderr pipe for task (%s)", err)
	}
	go func() {
		reader := bufio.NewReader(stderr)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			t.addOutput(TASK_OUTPUT_STDERR, strings.TrimSuffix(line, "\n"))
		}
	}()
}

func (t *Task) killProcessGroup(cmd *exec.Cmd) {
	// Take into account that the process could be NOT started yet
	if cmd.Process != nil {
		// "Use negative process group ID for killing the whole process group"
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil {
			// from here on out this application is in an undefined unrecoverable state
			// in most of my uses, it does not make sense to die here, as the process
			// will be restarted indefinitly and most possibly run into the same problem
			// just in case we can tell the task to die anyways if wanted
			if t.PanicIfLostControl {
				ulog.Panicf("cannot kill child process (%s) --> panicking as a last resord", err)
			} else {
				ulog.Errorf("cannot kill child process (%s)", err)
			}
		}
	}
}
