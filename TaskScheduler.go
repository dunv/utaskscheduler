package utaskscheduler

import (
	"fmt"
	"time"

	"github.com/dunv/concurrentList"
	"github.com/dunv/ulog"
	"github.com/google/uuid"
)

type SchedulerQueue string

const (
	TodoQueue       SchedulerQueue = "todo"
	InProgressQueue SchedulerQueue = "inProgress"
	DoneQueue       SchedulerQueue = "done"
)

type TaskScheduler struct {
	trigger        chan bool
	todoList       *concurrentList.ConcurrentList
	inProgressList *concurrentList.ConcurrentList
	doneList       *concurrentList.ConcurrentList
}

func NewTaskScheduler(progressChannel *chan TaskStatusUpdate) TaskScheduler {
	trigger := make(chan bool)
	todoList := concurrentList.NewConcurrentList()
	inProgressList := concurrentList.NewConcurrentList()
	doneList := concurrentList.NewConcurrentList()

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
				tasksRunInParallel := len(tasks.([]*Task))
				waitForAllToBeFinishedChannel := make(chan bool, tasksRunInParallel)
				for _, task := range tasks.([]*Task) {
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

	return TaskScheduler{
		trigger:        trigger,
		todoList:       todoList,
		inProgressList: inProgressList,
		doneList:       doneList,
	}
}

func (s TaskScheduler) triggerScheduling() {
	go func() {
		s.trigger <- true
	}()
}

func (s TaskScheduler) ScheduleParallel(tasks []*Task) {
	s.todoList.Push(tasks)
	s.triggerScheduling()
}

func (s TaskScheduler) Schedule(task *Task) {
	s.todoList.Push([]*Task{task})
	task.MarkAsScheduled()
	s.triggerScheduling()
}

func (s TaskScheduler) ScheduleWithoutQueue(task *Task) error {
	if s.todoList.Length() != 0 {
		return fmt.Errorf("There is already a task in the queue, not doing anything")
	}
	s.Schedule(task)
	return nil
}

func (s TaskScheduler) Stats() map[SchedulerQueue]interface{} {
	return map[SchedulerQueue]interface{}{
		TodoQueue:       s.todoList.Length(),
		InProgressQueue: s.inProgressList.Length(),
		DoneQueue:       s.doneList.Length(),
	}
}
func (s TaskScheduler) DetailedStats() map[SchedulerQueue]interface{} {
	todoStats := []map[string]interface{}{}
	for _, task := range s.todoList.GetWithFilter(func(item interface{}) bool { return true }) {
		todoStats = append(todoStats, map[string]interface{}{
			"taskGuid": task.([]*Task)[0].GUID(),
			"taskMeta": task.([]*Task)[0].Meta(),
		})
	}
	inProgressStats := []map[string]interface{}{}
	for _, task := range s.inProgressList.GetWithFilter(func(item interface{}) bool { return true }) {
		inProgressStats = append(inProgressStats, map[string]interface{}{
			"taskGuid":  task.([]*Task)[0].GUID(),
			"taskMeta":  task.([]*Task)[0].Meta(),
			"startedAt": task.([]*Task)[0].StartedAt(),
		})
	}
	doneStats := []map[string]interface{}{}
	for _, task := range s.doneList.GetWithFilter(func(item interface{}) bool { return true }) {
		doneStats = append(doneStats, map[string]interface{}{
			"taskGuid":   task.([]*Task)[0].GUID(),
			"taskMeta":   task.([]*Task)[0].Meta(),
			"startedAt":  task.([]*Task)[0].StartedAt(),
			"finishedAt": task.([]*Task)[0].FinishedAt(),
			"exitCode":   task.([]*Task)[0].ExitCode(),
			"error":      task.([]*Task)[0].Error(),
		})
	}

	return map[SchedulerQueue]interface{}{
		TodoQueue:       todoStats,
		InProgressQueue: inProgressStats,
		DoneQueue:       doneStats,
	}
}

func (s TaskScheduler) DoneTaskByUUID(taskGUID uuid.UUID) (*Task, error) {
	return s.TaskByUUID(DoneQueue, taskGUID)
}

func (s TaskScheduler) InProgressTaskByUUID(taskGUID uuid.UUID) (*Task, error) {
	return s.TaskByUUID(InProgressQueue, taskGUID)
}

func (s TaskScheduler) TodoTaskByUUID(taskGUID uuid.UUID) (*Task, error) {
	return s.TaskByUUID(TodoQueue, taskGUID)
}

func (s TaskScheduler) TaskByUUID(queueName SchedulerQueue, taskGUID uuid.UUID) (*Task, error) {
	var list *concurrentList.ConcurrentList
	switch queueName {
	case DoneQueue:
		list = s.doneList
	case TodoQueue:
		list = s.todoList
	case InProgressQueue:
		list = s.inProgressList
	}
	tasks := list.GetWithFilter(func(item interface{}) bool {
		for _, subItem := range item.([]*Task) {
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
	for index, subItem := range tasks[0].([]*Task) {
		if subItem.GUID() == taskGUID {
			correctIndex = index
		}
	}
	return tasks[0].([]*Task)[correctIndex], nil
}
