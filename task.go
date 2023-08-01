package utaskscheduler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/google/uuid"
)

type Task interface {
	String() string

	// Control
	Run()
	RunRedirectStatus(progressChannel *chan TaskStatusUpdate, returnChannel *chan bool)
	Cancel()
	MarkAsScheduled()

	// Get metadata
	GUID() uuid.UUID
	Command() string
	Args() []string
	WorkingDir() string
	Status() TaskStatus
	Env() []string
	Meta() interface{}

	// Get Status
	StartedAt() *time.Time
	FinishedAt() *time.Time
	ExitCode() int
	Error() error
	Executed() bool
	Output() []TaskOutput
}

type task struct {
	opts taskOptions

	status TaskStatus

	exitCode int
	executed bool

	// All output is collected here
	output []TaskOutput

	// if task is running, calling the cancelFunction should kill it
	cancelFunc context.CancelFunc

	// Information about the execution
	timedOut                    bool
	cancelled                   bool
	finishedWithoutInterference bool
	taskError                   error
	neededToKillOrphans         bool
	startedAt                   *time.Time
	finishedAt                  *time.Time

	// making this construct thread-safe
	outputLock   sync.Mutex
	statusLock   sync.Mutex
	progressLock sync.Mutex
	cancelLock   sync.Mutex

	outputConsumptionRoutines sync.WaitGroup
}

func NewTask(opts ...TaskOption) (Task, error) {
	mergedOpts := taskOptions{
		UID:             uuid.New(),
		timeout:         10 * time.Second,
		shellTermSignal: syscall.SIGKILL,
		logger:          defaultLogger{},
	}
	for _, opt := range opts {
		if err := opt.apply(&mergedOpts); err != nil {
			return nil, err
		}
	}

	if mergedOpts.taskType == "" {
		return nil, fmt.Errorf("required options WithFunction or WithShell not set")
	}

	return &task{
		opts: mergedOpts,
	}, nil
}

func NewFakeFailedTaskStatusUpdate(err error, taskMeta interface{}) TaskStatusUpdate {
	return TaskStatusUpdate{
		GUID:       uuid.New(),
		Status:     TASK_STATUS_FAILED,
		StartedAt:  uhelpers.PtrToTime(time.Now()),
		FinishedAt: uhelpers.PtrToTime(time.Now()),
		Executed:   true,
		ExitCode:   -1,
		Error:      err,
		Meta:       taskMeta,
	}
}

func (t *task) String() string {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()

	if t.opts.taskType == TASK_TYPE_FUNCTION {
		return fmt.Sprintf(`Task[type:%s timeout:%s status:%s]`, t.opts.taskType, t.opts.timeout, t.status)
	}

	cmd := t.opts.shellCommand
	if len(cmd) > 20 {
		cmd = cmd[0:17] + "..."
	}
	return fmt.Sprintf(`Task[type:%s timeout:%s command:"%s" status:%s]`, t.opts.taskType, t.opts.timeout, cmd, t.status)
}

func (t *task) Run() {
	if t.opts.taskType == TASK_TYPE_FUNCTION {
		t.runFunction()
		return
	}

	t.runShell()
}

func (t *task) RunRedirectStatus(progressChannel *chan TaskStatusUpdate, returnChannel *chan bool) {
	t.opts.progressChannel = progressChannel
	t.opts.returnChannel = returnChannel

	if t.opts.taskType == TASK_TYPE_FUNCTION {
		t.runFunction()
		return
	}

	t.runShell()
}

func (t *task) Cancel() {
	t.cancelLock.Lock()
	defer t.cancelLock.Unlock()

	if t.cancelFunc != nil {
		t.cancelFunc()
	}
}

func (t *task) runFunction() {
	functionOutputChannel := make(chan string)
	var ctx context.Context

	t.cancelLock.Lock()
	ctx, t.cancelFunc = context.WithTimeout(context.Background(), t.opts.timeout)
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
		exitCode := t.opts.fn(ctx, functionOutputChannel)

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

func (t *task) runShell() {
	// as exec.CommandContext predefinedly "only" sends cmd.Process.Kill(), we cannot use it here (https://golang.org/pkg/os/exec/#CommandContext)
	// we want the childProcess and all other descendants to be killed as well
	// https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
	cmd := exec.Command(t.opts.shellCommand, t.opts.shellArgs...)

	// Create our own context, so we can give a handle back which kills the process the way we want it to
	var ctx context.Context

	t.cancelLock.Lock()
	ctx, t.cancelFunc = context.WithCancel(context.Background())
	t.cancelLock.Unlock()

	// use process-group-id as handle instead of process-id
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// set working dir only if set in task
	cmd.Dir = t.opts.shellWorkingDir
	if t.opts.shellEnv != nil {
		cmd.Env = t.opts.shellEnv
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
	if t.opts.printStartAndEndInOutput {
		t.addOutput(TASK_OUTPUT_STDOUT, fmt.Sprintf("Running command '%s %s'", t.opts.shellCommand, strings.Join(t.opts.shellArgs, " ")))
	}
	t.markAsInProgress()

	// So we can return here, the waiting for the task to be done processing is handled in a goRoutine
	processEndedChannel := make(chan bool)
	go func() {
		// Wait until we have processed all the output so that we aren't missing any lines.
		// It is crucial that we wait for the output to finish before we call cmd.Wait, because
		// this will close the stdout and stderr pipes.
		t.outputConsumptionRoutines.Wait()

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

		// This could create problems (https://github.com/golang/go/issues/13987),
		// but I never noticed any in 4 years of production usage
		if !t.opts.shellDoNotKillOrphans {
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
	case <-time.After(t.opts.timeout):
		t.killProcessGroup(cmd)
		t.timedOut = true
		// After killing cmd.Wait() will return, this way we can cleanly exit here
		<-processEndedChannel
	case <-processEndedChannel:
		t.finishedWithoutInterference = true
	}
}

// Helper for processing output
func (t *task) addOutput(outputType TaskOutputType, outputString string) {
	t.outputLock.Lock()
	defer t.outputLock.Unlock()

	output := TaskOutput{
		TaskGUID: t.opts.UID,
		TaskMeta: t.opts.meta,
		Time:     time.Now(),
		Type:     outputType,
		Output:   outputString,
	}
	if t.opts.outputChannel != nil {
		*t.opts.outputChannel <- output
	}
	t.output = append(t.output, output)
}

// Helper for publishing into the returnChannel
func (t *task) returnTask(success bool) {
	if t.opts.returnChannel != nil {
		*t.opts.returnChannel <- success
	}
}

// Helper for publishing into the progressUpdateChannel
func (t *task) sendProgressUpdate() {
	t.progressLock.Lock()
	defer t.progressLock.Unlock()

	if t.opts.progressChannel != nil {
		t.statusLock.Lock()
		update := TaskStatusUpdate{
			GUID:       t.opts.UID,
			Status:     t.status,
			Meta:       t.opts.meta,
			StartedAt:  t.startedAt,
			FinishedAt: t.finishedAt,
			ExitCode:   t.exitCode,
			Error:      t.taskError,
			Executed:   t.executed,
		}
		t.statusLock.Unlock()
		*t.opts.progressChannel <- update
	}
}

// Helper for consuming stdErr and stdOut streams of a command
func (t *task) startConsumingOutputOfCommand(cmd *exec.Cmd) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.opts.logger.Errorf("Could not create stdout pipe for task (%s)\n", err)
	}

	t.outputConsumptionRoutines.Add(1)
	go func() {
		defer t.outputConsumptionRoutines.Done()
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			line = strings.TrimSuffix(line, "\n")
			if err == nil {
				t.addOutput(TASK_OUTPUT_STDOUT, line)
				continue
			}
			if err == io.EOF {
				if len(line) > 0 {
					t.addOutput(TASK_OUTPUT_STDOUT, line)
				}
			} else {
				t.opts.logger.Errorf("unexpected error when reading from stdout (%s)", err)
			}
			return
		}
	}()

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.opts.logger.Errorf("Could not create stderr pipe for task (%s)", err)
	}

	t.outputConsumptionRoutines.Add(1)
	go func() {
		defer t.outputConsumptionRoutines.Done()
		reader := bufio.NewReader(stderr)
		for {
			line, err := reader.ReadString('\n')
			line = strings.TrimSuffix(line, "\n")
			if err == nil {
				t.addOutput(TASK_OUTPUT_STDERR, strings.TrimSuffix(line, "\n"))
				continue
			}
			if err == io.EOF {
				if len(line) > 0 {
					t.addOutput(TASK_OUTPUT_STDOUT, line)
				}
			} else {
				t.opts.logger.Errorf("unexpected error when reading from stderr (%s)", err)
			}
			return
		}
	}()
}

func (t *task) killProcessGroup(cmd *exec.Cmd) {
	// Take into account that the process could be NOT started yet
	if cmd.Process != nil {

		// "Use negative process group ID for killing the whole process group"
		err := syscall.Kill(-cmd.Process.Pid, t.opts.shellTermSignal)
		if err != nil {
			// from here on out this application is in an undefined unrecoverable state
			// in most of my uses, it does not make sense to die here, as the process
			// will be restarted indefinitly and most possibly run into the same problem
			// just in case we can tell the task to die anyways if wanted
			if t.opts.shellPanicIfLostControl {
				t.opts.logger.Errorf("cannot kill child process (%s) --> panicking as a last resord", err)
				panic(fmt.Sprintf("cannot kill child process (%s) --> panicking as a last resord", err))
			} else {
				t.opts.logger.Errorf("cannot kill child process (%s)", err)
			}
		}
	}
}

func (t *task) MarkAsScheduled() {
	t.statusLock.Lock()
	t.status = TASK_STATUS_SCHEDULED
	t.statusLock.Unlock()

	t.sendProgressUpdate()
}

func (t *task) markAsInProgress() {
	t.statusLock.Lock()
	t.startedAt = uhelpers.PtrToTime(time.Now())
	t.status = TASK_STATUS_IN_PROGRESS
	t.statusLock.Unlock()

	t.sendProgressUpdate()
}

func (t *task) markAsFailed(exitCode int, err error, output ...string) {
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

func (t *task) markAsSuccessful(output ...string) {
	t.statusLock.Lock()
	t.finishedAt = uhelpers.PtrToTime(time.Now())
	t.executed = true
	t.exitCode = 0
	t.status = TASK_STATUS_SUCCESS
	t.statusLock.Unlock()

	if t.opts.printStartAndEndInOutput && len(output) > 0 {
		t.addOutput(TASK_OUTPUT_STDOUT, strings.Join(output, ", "))
	}
	t.returnTask(true)
	t.sendProgressUpdate()
}

func (t *task) GUID() uuid.UUID {
	return t.opts.UID
}

func (t *task) Command() string {
	return t.opts.shellCommand
}

func (t *task) Args() []string {
	return t.opts.shellArgs
}

func (t *task) WorkingDir() string {
	return t.opts.shellWorkingDir
}

func (t *task) Env() []string {
	return t.opts.shellEnv
}

func (t *task) Meta() interface{} {
	return t.opts.meta
}

func (t *task) Status() TaskStatus {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.status
}

func (t *task) StartedAt() *time.Time {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.startedAt
}

func (t *task) FinishedAt() *time.Time {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.finishedAt
}

func (t *task) ExitCode() int {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.exitCode
}

func (t *task) Error() error {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.taskError
}

func (t *task) Executed() bool {
	t.statusLock.Lock()
	defer t.statusLock.Unlock()
	return t.executed
}

func (t *task) Output() []TaskOutput {
	t.outputLock.Lock()
	defer t.outputLock.Unlock()

	copy := []TaskOutput{}
	copy = append(copy, t.output...)
	return copy
}
