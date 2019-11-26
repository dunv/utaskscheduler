package main

import (
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/ulog"
	task "github.com/dunv/utaskscheduler"
)

func main() {
	progressChannel := make(chan task.Task)
	outputChannel := make(chan task.TaskOutput)
	scheduler := task.NewTaskScheduler(progressChannel)
	taskDoneChannel := make(chan bool)

	go func(outputChannel chan task.TaskOutput) {
		for output := range outputChannel {
			ulog.Info(output)
		}
	}(outputChannel)

	go func(progressChannel chan task.Task) {
		for progress := range progressChannel {
			ulog.Info(progress)
			if progress.Status == task.TASK_STATUS_SUCCESS || progress.Status == task.TASK_STATUS_FAILED {
				taskDoneChannel <- true
			}
		}
	}(progressChannel)

	// executablePath, err := filepath.Abs(filepath.Join("..", "ignoreAllSignals", "ignoreAllSignals"))
	// if err != nil {
	// 	panic(err)
	// }
	// task := task.NewShellTask("sudo", []string{executablePath}, uhelpers.PtrToDuration(5*time.Second), &outputChannel)

	task := task.NewShellTask("/bin/sh", []string{"-c", "\"echo hello\""}, uhelpers.PtrToDuration(5*time.Second), &outputChannel)
	scheduler.Schedule(task)

	<-taskDoneChannel
	ulog.Info("end")
	time.Sleep(time.Minute)
}
