package utaskscheduler_test

import (
	"testing"
	"time"

	uts "github.com/dunv/utaskscheduler"
	"github.com/stretchr/testify/require"
)

func TestScheduleMultipleShellTasks(t *testing.T) {
	progressChannel, outputChannel := setupTaskProgress()
	scheduler := uts.NewTaskScheduler(&progressChannel)

	task, err := uts.NewTask(
		uts.WithTimeout(time.Second),
		uts.WithOutputChannel(outputChannel),
		uts.WithShell("echo", "halloWelt"),
	)
	require.NoError(t, err)

	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)
	scheduler.Schedule(task)

	for scheduler.Stats()[uts.TodoQueue] > 0 {
		time.Sleep(200 * time.Millisecond)
	}
}
