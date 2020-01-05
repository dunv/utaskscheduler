package utaskscheduler

import (
	"testing"
	"time"

	"github.com/dunv/uhelpers"
)

func TestScheduleMultipleShellTasks(t *testing.T) {
	progressChannel, outputChannel := setupTaskProgress()
	scheduler := NewTaskScheduler(progressChannel)

	task := NewShellTask("echo", []string{"halloWelt"}, uhelpers.PtrToDuration(time.Second), outputChannel)
	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)

	for scheduler.todoList.Length() > 0 {
		time.Sleep(200 * time.Millisecond)
	}

}
