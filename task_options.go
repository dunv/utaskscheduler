package utaskscheduler

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type taskOptions struct {
	// common
	UID                      uuid.UUID
	timeout                  time.Duration
	outputChannel            *chan TaskOutput
	progressChannel          *chan TaskStatusUpdate
	returnChannel            *chan bool
	meta                     interface{}
	printStartAndEndInOutput bool
	taskType                 TaskType
	logger                   Logger

	// functionTask
	fn func(ctx context.Context, outputChannel chan string) int

	// shellTask
	shellCommand            string
	shellArgs               []string
	shellEnv                []string
	shellWorkingDir         string
	shellTermSignal         syscall.Signal
	shellDoNotKillOrphans   bool
	shellPanicIfLostControl bool
}

type TaskOption interface {
	apply(*taskOptions) error
}
type funcTaskOption struct {
	f func(*taskOptions) error
}

func (fdo *funcTaskOption) apply(do *taskOptions) error {
	return fdo.f(do)
}

func newFuncTaskOption(f func(*taskOptions) error) *funcTaskOption {
	return &funcTaskOption{f: f}
}

func WithUID(uid uuid.UUID) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.UID = uid
		return nil
	})
}

func WithTimeout(timeout time.Duration) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.timeout = timeout
		return nil
	})
}

func WithOutputChannel(outputChannel chan TaskOutput) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.outputChannel = &outputChannel
		return nil
	})
}

func WithProgressChannel(progressChannel chan TaskStatusUpdate) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.progressChannel = &progressChannel
		return nil
	})
}

func WithReturnChannel(returnChannel chan bool) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.returnChannel = &returnChannel
		return nil
	})
}

func WithMeta(meta interface{}) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.meta = meta
		return nil
	})
}

func WithPrintStartAndEndInOutput() TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.printStartAndEndInOutput = true
		return nil
	})
}

func WithLogger(logger Logger) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		o.logger = logger
		return nil
	})
}

func WithFunction(fn func(ctx context.Context, outputChannel chan string) int) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.shellCommand != "" || len(o.shellArgs) != 0 {
			return fmt.Errorf("cannot set both function and shell command")
		}
		o.taskType = TASK_TYPE_FUNCTION
		o.fn = fn
		return nil
	})
}

func WithShell(command string, args ...string) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("cannot set both function and shell command")
		}
		o.taskType = TASK_TYPE_SHELL
		o.shellCommand = command
		o.shellArgs = args
		return nil
	})
}

func WithShellEnvironment(env []string) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("ShellEnvironment is not compatible with function")
		}
		o.shellEnv = env
		return nil
	})
}

func WithShellWorkingDir(workingDir string) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("ShellWorkingDir is not compatible with function")
		}
		o.shellWorkingDir = workingDir
		return nil
	})
}

func WithShellTermSignal(termSignal syscall.Signal) TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("ShellTermSignal is not compatible with function")
		}
		o.shellTermSignal = termSignal
		return nil
	})
}

func WithShellDoNotKillOrphans() TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("ShellDoNotKillOrphans is not compatible with function")
		}
		o.shellDoNotKillOrphans = true
		return nil
	})
}

func WithShellPanicIfLostControl() TaskOption {
	return newFuncTaskOption(func(o *taskOptions) error {
		if o.fn != nil {
			return fmt.Errorf("ShellPanicIfLostControl is not compatible with function")
		}
		o.shellPanicIfLostControl = true
		return nil
	})
}
