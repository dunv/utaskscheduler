package utaskscheduler

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/ulog"
	"github.com/google/uuid"
)

type Task struct {
	// General attributes
	taskGUID uuid.UUID
	taskType TaskType
	status   TaskStatus
	timeout  time.Duration

	// Shell-Task attributes
	env                      []string
	command                  string
	workingDir               *string
	args                     []string
	exitCode                 int
	executed                 bool
	printStartAndEndInOutput bool

	// If not nil ReturnChannel will receive true or false, depending if the execution was successful
	returnChannel *chan bool

	// If not nil ProgressChannel will receive a copy of the task so its progress can be monitored
	progressChannel *chan TaskStatusUpdate

	// Function attributes
	function func(ctx context.Context, outputChannel chan string) int

	// If not nil: all output is streamed
	outputChannel *chan TaskOutput

	// All output is collected here
	output []TaskOutput

	// Variable for transporting metainformation in this task (i.e. what does it belong to etc.)
	taskMeta interface{}

	// if task is running, calling the cancelFunction should kill it
	cancelFunc context.CancelFunc

	// Information about the execution
	timedOut                    bool
	cancelled                   bool
	finishedWithoutInterference bool
	taskError                   error
	PanicIfLostControl          bool
	DoNotKillOrphans            bool
	neededToKillOrphans         bool
	startedAt                   *time.Time
	finishedAt                  *time.Time

	// making this construct thread-safe
	// TODO: find a way of doing this without locks by using channels and selects
	outputLock   sync.Mutex
	statusLock   sync.Mutex
	progressLock sync.Mutex
	cancelLock   sync.Mutex

	outputConsumptionRoutines sync.WaitGroup
}

func NewShellTask(
	command string,
	args []string,
	timeout *time.Duration,
	outputChannel *chan TaskOutput,
	printStartAndEndInOutputList ...bool,
) *Task {
	usedTimeout := 10 * time.Second
	if timeout != nil {
		usedTimeout = *timeout
	}

	printStartAndEndInOutput := false
	if len(printStartAndEndInOutputList) == 1 {
		printStartAndEndInOutput = printStartAndEndInOutputList[0]
	}

	return &Task{
		taskGUID:                 uuid.New(),
		taskType:                 TASK_TYPE_SHELL,
		outputChannel:            outputChannel,
		command:                  command,
		args:                     args,
		timeout:                  usedTimeout,
		printStartAndEndInOutput: printStartAndEndInOutput,
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
		taskGUID:      uuid.New(),
		taskType:      TASK_TYPE_FUNCTION,
		outputChannel: outputChannel,
		function:      function,
		timeout:       usedTimeout,
	}
}

func NewFakeFailedTaskStatusUpdate(err error, taskMeta interface{}) TaskStatusUpdate {
	return TaskStatusUpdate{
		GUID:       uuid.New().String(),
		Status:     TASK_STATUS_FAILED,
		StartedAt:  uhelpers.PtrToTime(time.Now()),
		FinishedAt: uhelpers.PtrToTime(time.Now()),
		Executed:   true,
		ExitCode:   -1,
		Error:      err,
		Meta:       taskMeta,
	}
}

func (t *Task) String() string {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()

	cmd := t.command
	if len(cmd) > 20 {
		cmd = cmd[0:17] + "..."
	}
	if t.taskType == TASK_TYPE_FUNCTION {
		return fmt.Sprintf(`Task[type:%s timeout:%s status:%s]`, t.taskType, t.timeout, t.status)
	} else {
		return fmt.Sprintf(`Task[type:%s timeout:%s command:"%s" status:%s]`, t.taskType, t.timeout, cmd, t.status)
	}
}

func (t *Task) Run(progressChannel *chan TaskStatusUpdate, returnChannel *chan bool) {
	t.returnChannel = returnChannel

	t.progressLock.Lock()
	t.progressChannel = progressChannel
	t.progressLock.Unlock()

	switch t.taskType {
	case TASK_TYPE_SHELL:
		t.runShell()
	default:
		t.runFunction()
	}
}

func (t *Task) Cancel() {
	t.cancelLock.Lock()
	defer t.cancelLock.Unlock()

	if t.cancelFunc != nil {
		t.cancelFunc()
	}
}

func (t *Task) runFunction() {
	functionOutputChannel := make(chan string)
	var ctx context.Context

	t.cancelLock.Lock()
	ctx, t.cancelFunc = context.WithTimeout(context.Background(), t.timeout)
	t.cancelLock.Unlock()

	go func(functionOutputChannel chan string) {
		for output := range functionOutputChannel {
			t.addOutput(TASK_OUTPUT_STDOUT, output)
		}
	}(functionOutputChannel)

	go func(ctx context.Context, functionOutputChannel chan string) {
		t.markAsInProgress()

		// We are not adding a timeout here, so if a function runs indefinitely this is blocking as well.
		// Canceling needs to be handled within function
		exitCode := t.function(ctx, functionOutputChannel)

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
}

func (t *Task) runShell() {
	// as exec.CommandContext predefinedly "only" sends cmd.Process.Kill(), we cannot use it here (https://golang.org/pkg/os/exec/#CommandContext)
	// we want the childProcess and all other descendants to be killed as well
	// https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
	cmd := exec.Command(t.command, t.args...)

	// Create our own context, so we can give a handle back which kills the process the way we want it to
	var ctx context.Context

	t.cancelLock.Lock()
	ctx, t.cancelFunc = context.WithCancel(context.Background())
	t.cancelLock.Unlock()

	// use process-group-id as handle instead of process-id
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// set working dir only if set in task
	if t.workingDir != nil {
		cmd.Dir = *t.workingDir
	}

	if t.env != nil {
		cmd.Env = t.env
	}

	// Start listeners for output
	t.startConsumingOutputOfCommand(cmd)

	// actually start the task
	err := cmd.Start()

	// if the task could not be started -> set task attrs, publish err to output and return
	if err != nil {
		go func() {
			t.markAsFailed(-1, err, fmt.Sprintf("Error starting (%s)", err.Error()))
		}()
		return
	}

	// initial output (helps for debugging what is actually being run here)
	if t.printStartAndEndInOutput {
		t.addOutput(TASK_OUTPUT_STDOUT, fmt.Sprintf("Running command '%s %s'", t.command, strings.Join(t.args, " ")))
	}
	t.markAsInProgress()

	// So we can return here, the waiting for the task to be done processing is handled in a goRoutine
	processEndedChannel := make(chan bool)
	go func() {
		err := cmd.Wait()
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
				t.neededToKillOrphans = true
			}
		}
	}()

	// Implement our own timeout (see above for why)
	select {
	case <-ctx.Done():
		t.killProcessGroup(cmd)
		t.cancelled = true
		// After killing cmd.Wait() will return, this way we can cleanly exit here
		<-processEndedChannel
	case <-time.After(t.timeout):
		t.killProcessGroup(cmd)
		t.timedOut = true
		// After killing cmd.Wait() will return, this way we can cleanly exit here
		<-processEndedChannel
	case <-processEndedChannel:
		t.finishedWithoutInterference = true
	}

	// Wait until we have processed all the output so that we aren't missing any
	// lines
	t.outputConsumptionRoutines.Wait()
}

// Helper for processing output
func (t *Task) addOutput(outputType TaskOutputType, outputString string) {
	t.outputLock.Lock()
	defer t.outputLock.Unlock()

	output := TaskOutput{
		TaskGUID: t.taskGUID,
		TaskMeta: t.taskMeta,
		Time:     time.Now(),
		Type:     outputType,
		Output:   outputString,
	}
	if t.outputChannel != nil {
		*t.outputChannel <- output
	}
	t.output = append(t.output, output)
}

// Helper for publishing into the returnChannel
func (t *Task) returnTask(success bool) {
	if t.returnChannel != nil {
		*t.returnChannel <- success
	}
}

// Helper for publishing into the progressUpdateChannel
func (t *Task) sendProgressUpdate() {
	t.progressLock.Lock()
	defer t.progressLock.Unlock()

	if t.progressChannel != nil {
		t.statusLock.Lock()
		update := TaskStatusUpdate{
			GUID:       t.taskGUID.String(),
			Status:     t.status,
			Meta:       t.taskMeta,
			StartedAt:  t.startedAt,
			FinishedAt: t.finishedAt,
			ExitCode:   t.exitCode,
			Error:      t.taskError,
			Executed:   t.executed,
		}
		t.statusLock.Unlock()
		*t.progressChannel <- update
	}
}

// Helper for consuming stdErr and stdOut streams of a command
func (t *Task) startConsumingOutputOfCommand(cmd *exec.Cmd) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ulog.Errorf("Could not create stdout pipe for task (%s)", err)
	}

	t.outputConsumptionRoutines.Add(1)
	go func() {
		defer t.outputConsumptionRoutines.Done()
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

	t.outputConsumptionRoutines.Add(1)
	go func() {
		defer t.outputConsumptionRoutines.Done()
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

func (t *Task) MarkAsScheduled() {
	t.statusLock.Lock()
	t.status = TASK_STATUS_SCHEDULED
	t.statusLock.Unlock()

	t.sendProgressUpdate()
}

func (t *Task) markAsInProgress() {
	t.statusLock.Lock()
	t.startedAt = uhelpers.PtrToTime(time.Now())
	t.status = TASK_STATUS_IN_PROGRESS
	t.statusLock.Unlock()

	t.sendProgressUpdate()
}

func (t *Task) markAsFailed(exitCode int, err error, output ...string) {
	t.statusLock.Lock()
	t.finishedAt = uhelpers.PtrToTime(time.Now())
	t.executed = true
	t.exitCode = exitCode
	t.taskError = err
	t.status = TASK_STATUS_FAILED
	t.statusLock.Unlock()

	if len(output) > 0 {
		t.addOutput(TASK_OUTPUT_STDERR, strings.Join(output, ", "))
	}
	t.returnTask(false)
	t.sendProgressUpdate()
}

func (t *Task) markAsSuccessful(output ...string) {
	t.statusLock.Lock()
	t.finishedAt = uhelpers.PtrToTime(time.Now())
	t.executed = true
	t.exitCode = 0
	t.status = TASK_STATUS_SUCCESS
	t.statusLock.Unlock()

	if t.printStartAndEndInOutput && len(output) > 0 {
		t.addOutput(TASK_OUTPUT_STDOUT, strings.Join(output, ", "))
	}
	t.returnTask(true)
	t.sendProgressUpdate()
}

func (t *Task) GUID() uuid.UUID {
	return t.taskGUID
}

func (t *Task) Command() string {
	return t.command
}

func (t *Task) Args() []string {
	return t.args
}

func (t *Task) SetWorkingDir(dir string) error {
	if t.StartedAt() != nil {
		return fmt.Errorf("task already in progress, cannot set working dir")
	}

	t.workingDir = uhelpers.PtrToString(dir)
	return nil
}

func (t *Task) Status() TaskStatus {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.status
}

func (t *Task) SetEnv(env []string) error {
	if t.StartedAt() != nil {
		return fmt.Errorf("task already in progress, cannot set env")
	}

	t.env = env
	return nil
}

func (t *Task) Env() []string {
	return t.env
}

func (t *Task) SetMeta(meta interface{}) error {
	if t.StartedAt() != nil {
		return fmt.Errorf("task already in progress, cannot set meta")
	}

	t.taskMeta = meta
	return nil
}

func (t *Task) Meta() interface{} {
	return t.taskMeta
}

func (t *Task) StartedAt() *time.Time {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.startedAt
}

func (t *Task) FinishedAt() *time.Time {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.finishedAt
}

func (t *Task) ExitCode() int {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.exitCode
}

func (t *Task) Error() error {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.taskError
}

func (t *Task) Executed() bool {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.executed
}

func (t *Task) Output() []TaskOutput {
	t.outputLock.Lock()
	defer t.outputLock.Unlock()

	copy := []TaskOutput{}
	copy = append(copy, t.output...)
	return copy
}
