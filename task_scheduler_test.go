package utaskscheduler_test

import (
	"testing"
	"time"

	"github.com/dunv/uhelpers"
	"github.com/dunv/utaskscheduler"
)

func TestScheduleMultipleShellTasks(t *testing.T) {
	progressChannel, outputChannel := setupTaskProgress()
	scheduler := utaskscheduler.NewTaskScheduler(progressChannel)

	task := utaskscheduler.NewShellTask("echo", []string{"halloWelt"}, uhelpers.PtrToDuration(time.Second), outputChannel)
	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)

	for scheduler.Stats()[utaskscheduler.TodoQueue] > 0 {
		time.Sleep(200 * time.Millisecond)
	}

}
