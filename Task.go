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
	// General attributes
	TaskGUID uuid.UUID
	Type     TaskType
	Status   TaskStatus
	Timeout  time.Duration

	// Shell-Task attributes
	Command    string
	WorkingDir *string
	Args       []string
	ExitCode   int
	Executed   bool

	// If not nil ReturnChannel will receive true or false, depending if the execution was successful
	ReturnChannel *chan bool

	// If not nil ProgressChannel will receive a copy of the task so its progress can be monitored
	ProgressChannel *chan Task

	// Function attributes
	Function func(ctx context.Context, outputChannel chan string) int

	// If not nil: all output is streamed
	OutputChannel *chan TaskOutput

	// All output is collected here
	Output []TaskOutput

	// Variable for transporting metainformation in this task (i.e. what does it belong to etc.)
	TaskMeta interface{}

	// Information about the execution
	TimedOut                    bool
	Cancelled                   bool
	FinishedWithoutInterference bool
	Error                       error
	PanicIfLostControl          bool
	DoNotKillOrphans            bool
	NeededToKillOrphans         bool
	StartedAt                   *time.Time
	FinishedAt                  *time.Time
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

func NewFunctionTask(
	function func(ctx context.Context, outputChannel chan string) int,
	timeout *time.Duration,
	outputChannel *chan TaskOutput,
) *Task {
	usedTimeout := 10 * time.Second
	if timeout != nil {
		usedTimeout = *timeout
	}

	return &Task{
		TaskGUID:      uuid.New(),
		Type:          TASK_TYPE_FUNCTION,
		OutputChannel: outputChannel,
		Function:      function,
		Timeout:       usedTimeout,
	}
}

func (t Task) String() string {
	cmd := t.Command
	if len(cmd) > 20 {
		cmd = cmd[0:17] + "..."
	}
	if t.Type == TASK_TYPE_FUNCTION {
		return fmt.Sprintf(`Task[type:%s timeout:%s status:%s]`, t.Type, t.Timeout, t.Status)
	} else {
		return fmt.Sprintf(`Task[type:%s timeout:%s command:"%s" status:%s]`, t.Type, t.Timeout, cmd, t.Status)
	}
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
	functionOutputChannel := make(chan string)
	ctx, cancelFunction := context.WithTimeout(context.Background(), t.Timeout)

	go func(functionOutputChannel chan string) {
		for output := range functionOutputChannel {
			t.addOutput(TASK_OUTPUT_STDOUT, output)
		}
	}(functionOutputChannel)

	go func(ctx context.Context, functionOutputChannel chan string) {
		t.markAsInProgress()

		// We are not adding a timeout here, so if a function runs indefinitely this is blocking as well.
		// Canceling needs to be handled within function
		exitCode := t.Function(ctx, functionOutputChannel)

		close(functionOutputChannel)

		// If context has timed out -> set exitCode manually and add output
		if ctx.Err() != nil {
			exitCode = -1
			t.addOutput(TASK_OUTPUT_STDERR, ctx.Err().Error())
		}

		if exitCode == 0 {
			t.markAsSuccessful()
		} else {
			t.markAsFailed(exitCode, ctx.Err())
		}
	}(ctx, functionOutputChannel)
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

	// Start listeners for output
	t.startConsumingOutputOfCommand(cmd)

	// actually start the task
	err := cmd.Start()

	// if the task could not be started -> set task attrs, publish err to output and return
	if err != nil {
		t.markAsFailed(-1, err, fmt.Sprintf("Error starting (%s)", err.Error()))
		return cancelFunc
	}

	// initial output (helps for debugging what is actually being run here)
	t.addOutput(TASK_OUTPUT_STDOUT, fmt.Sprintf("Running command '%s %s'", t.Command, strings.Join(t.Args, " ")))
	t.markAsInProgress()

	// So we can return here, the waiting for the task to be done processing is handled in a goRoutine
	processEndedChannel := make(chan bool)
	go func() {
		err := cmd.Wait()
		t.Executed = true
		t.FinishedAt = uhelpers.PtrToTime(time.Now())
		processEndedChannel <- true

		if err != nil {
			// Default to exit-code -1
			exitCode := -1
			switch parsedErr := err.(type) {
			case *exec.ExitError:
				exitCode = parsedErr.ExitCode()
			}

			t.markAsFailed(exitCode, err, fmt.Sprintf("Error executing (%s)", err.Error()))
		} else {
			t.markAsSuccessful("Done executing")
		}

		// TODO: check if this creates problems (https://github.com/golang/go/issues/13987)
		if !t.DoNotKillOrphans {
			// Make sure there are no remnants left
			err = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			// fmt.Printf("%+v %T\n", err, err)
			if err == nil {
				t.addOutput(TASK_OUTPUT_STDOUT, "Killed orphaned children successfully")
				t.NeededToKillOrphans = true
			}
		}
	}()

	// Implement our own timeout (see above for why)
	select {
	case <-ctx.Done():
		t.killProcessGroup(cmd)
		t.Cancelled = true
		// After killing cmd.Wait() will return, this way we can cleanly exit here
		<-processEndedChannel
	case <-time.After(t.Timeout):
		t.killProcessGroup(cmd)
		t.TimedOut = true
		// After killing cmd.Wait() will return, this way we can cleanly exit here
		<-processEndedChannel
	case <-processEndedChannel:
		t.FinishedWithoutInterference = true
	}

	return cancelFunc
}

// Helper for processing output
func (t *Task) addOutput(outputType TaskOutputType, outputString string) {
	output := TaskOutput{
		TaskGUID: t.TaskGUID,
		TaskMeta: t.TaskMeta,
		Time:     time.Now(),
		Type:     outputType,
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
		*t.ReturnChannel <- success
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

func (t *Task) markAsInProgress() {
	t.StartedAt = uhelpers.PtrToTime(time.Now())
	t.Status = TASK_STATUS_IN_PROGRESS
	t.sendProgressUpdate()
}

func (t *Task) markAsFailed(exitCode int, err error, output ...string) {
	t.FinishedAt = uhelpers.PtrToTime(time.Now())
	t.Executed = true
	t.ExitCode = exitCode
	t.Error = err
	if len(output) > 0 {
		t.addOutput(TASK_OUTPUT_STDERR, strings.Join(output, ", "))
	}
	t.Status = TASK_STATUS_FAILED
	t.returnTask(false)
	t.sendProgressUpdate()
}

func (t *Task) markAsSuccessful(output ...string) {
	t.FinishedAt = uhelpers.PtrToTime(time.Now())
	t.Executed = true
	t.ExitCode = 0
	if len(output) > 0 {
		t.addOutput(TASK_OUTPUT_STDOUT, strings.Join(output, ", "))
	}
	t.Status = TASK_STATUS_SUCCESS
	t.returnTask(true)
	t.sendProgressUpdate()
}
