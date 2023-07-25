package utaskscheduler

import (
	"fmt"
	"time"

	concurrentList "github.com/dunv/concurrentList/v2"
	"github.com/dunv/ulog"
	"github.com/google/uuid"
)

type SchedulerQueue string

const (
	TodoQueue       SchedulerQueue = "todo"
	InProgressQueue SchedulerQueue = "inProgress"
	DoneQueue       SchedulerQueue = "done"
)

type TaskScheduler interface {
	ScheduleParallel(t []Task)
	Schedule(t Task)
	ScheduleWithoutQueue(t Task) error
	Stats() map[SchedulerQueue]int
	DetailedStats() map[SchedulerQueue][]TaskStatusUpdate
	TaskByUUID(queueName SchedulerQueue, taskGUID uuid.UUID) (Task, error)
}

type taskScheduler struct {
	trigger        chan bool
	todoList       *concurrentList.ConcurrentList[[]Task]
	inProgressList *concurrentList.ConcurrentList[[]Task]
	doneList       *concurrentList.ConcurrentList[[]Task]
}

func NewTaskScheduler(progressChannel *chan TaskStatusUpdate) TaskScheduler {
	trigger := make(chan bool)
	todoList := concurrentList.NewConcurrentList[[]Task]()
	inProgressList := concurrentList.NewConcurrentList[[]Task]()
	doneList := concurrentList.NewConcurrentList[[]Task]()

	// Actual Scheduler
	go func() {
		// Wait for trigger
		for range trigger {
			// Get tasks
			tasks, err := todoList.Shift()
			if err == nil {
				// Keep track
				inProgressList.Push(tasks)

				// Run tasks
				tasksRunInParallel := len(tasks)
				waitForAllToBeFinishedChannel := make(chan bool, tasksRunInParallel)
				for _, task := range tasks {
					// progressChannel <- *task
					task.Run(progressChannel, &waitForAllToBeFinishedChannel)
				}

				// Wait for group to finish
				counter := tasksRunInParallel
				for range waitForAllToBeFinishedChannel {
					counter--
					if counter == 0 {
						break
					}
				}

				// Keep track
				_, err = inProgressList.Shift()
				if err != nil {
					ulog.Error("No tasks in progress, but done: this should never happen!")
				}

				doneList.Push(tasks)
			} else {
				ulog.Error("No tasks in list, but triggered: this should never happen!")
				time.Sleep(1 * time.Second)
			}
		}
	}()

	return taskScheduler{
		trigger:        trigger,
		todoList:       todoList,
		inProgressList: inProgressList,
		doneList:       doneList,
	}
}

func (s taskScheduler) triggerScheduling() {
	go func() {
		s.trigger <- true
	}()
}

func (s taskScheduler) ScheduleParallel(t []Task) {
	s.todoList.Push(t)
	s.triggerScheduling()
}

func (s taskScheduler) Schedule(t Task) {
	s.todoList.Push([]Task{t})
	t.MarkAsScheduled()
	s.triggerScheduling()
}

func (s taskScheduler) ScheduleWithoutQueue(t Task) error {
	if s.todoList.Length() != 0 {
		return fmt.Errorf("There is already a task in the queue, not doing anything")
	}
	s.Schedule(t)
	return nil
}

func (s taskScheduler) Stats() map[SchedulerQueue]int {
	return map[SchedulerQueue]int{
		TodoQueue:       s.todoList.Length(),
		InProgressQueue: s.inProgressList.Length(),
		DoneQueue:       s.doneList.Length(),
	}
}

func (s taskScheduler) DetailedStats() map[SchedulerQueue][]TaskStatusUpdate {
	todoStats := []TaskStatusUpdate{}
	for _, task := range s.todoList.GetWithFilter(func(item []Task) bool { return true }) {
		todoStats = append(todoStats, TaskStatusUpdate{
			GUID: task[0].GUID(),
			Meta: task[0].Meta(),
		})
	}
	inProgressStats := []TaskStatusUpdate{}
	for _, task := range s.inProgressList.GetWithFilter(func(item []Task) bool { return true }) {
		inProgressStats = append(inProgressStats, TaskStatusUpdate{
			GUID:      task[0].GUID(),
			Meta:      task[0].Meta(),
			StartedAt: task[0].StartedAt(),
		})
	}
	doneStats := []TaskStatusUpdate{}
	for _, task := range s.doneList.GetWithFilter(func(item []Task) bool { return true }) {
		doneStats = append(doneStats, TaskStatusUpdate{
			GUID:       task[0].GUID(),
			Meta:       task[0].Meta(),
			StartedAt:  task[0].StartedAt(),
			FinishedAt: task[0].FinishedAt(),
			ExitCode:   task[0].ExitCode(),
			Error:      task[0].Error(),
		})
	}

	return map[SchedulerQueue][]TaskStatusUpdate{
		TodoQueue:       todoStats,
		InProgressQueue: inProgressStats,
		DoneQueue:       doneStats,
	}
}

func (s taskScheduler) TaskByUUID(queueName SchedulerQueue, taskGUID uuid.UUID) (Task, error) {
	var list *concurrentList.ConcurrentList[[]Task]
	switch queueName {
	case DoneQueue:
		list = s.doneList
	case TodoQueue:
		list = s.todoList
	case InProgressQueue:
		list = s.inProgressList
	}
	tasks := list.GetWithFilter(func(item []Task) bool {
		for _, subItem := range item {
			if subItem.GUID() == taskGUID {
				return true
			}
		}
		return false
	})
	if len(tasks) == 0 {
		return nil, fmt.Errorf("TaskGUID %s not found in %s tasks", taskGUID, queueName)
	} else if len(tasks) > 1 {
		return nil, fmt.Errorf("TaskGUID %s found multiple times in %s tasks", taskGUID, queueName)
	}

	var correctIndex int
	for index, subItem := range tasks[0] {
		if subItem.GUID() == taskGUID {
			correctIndex = index
		}
	}
	return tasks[0][correctIndex], nil
}
