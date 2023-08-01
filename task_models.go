package utaskscheduler

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type TaskStatus string

const (
	TASK_STATUS_SCHEDULED   TaskStatus = "SCHEDULED"
	TASK_STATUS_IN_PROGRESS TaskStatus = "IN_PROGRESS"
	TASK_STATUS_SUCCESS     TaskStatus = "SUCCESS"
	TASK_STATUS_FAILED      TaskStatus = "FAILED"
)

type TaskStatusUpdate struct {
	GUID       uuid.UUID   `json:"guid"`
	Status     TaskStatus  `json:"status"`
	Meta       interface{} `json:"meta,omitempty"`
	StartedAt  *time.Time  `json:"startedAt,omitempty"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	ExitCode   int         `json:"exitCode"`
	Error      error       `json:"error,omitempty"`
	Executed   bool        `json:"executed"`
}

func (t TaskStatusUpdate) String() string {
	return fmt.Sprintf(`TaskStatusUpdate[guid:%s status:%s exitCode:%d err:%v executed:%t ]`,
		t.GUID, t.Status, t.ExitCode, t.Error, t.Executed,
	)
}

type TaskType string

const (
	TASK_TYPE_SHELL    TaskType = "SHELL"
	TASK_TYPE_FUNCTION TaskType = "FUNCTION"
)

type TaskOutputType string

const (
	TASK_OUTPUT_STDOUT TaskOutputType = "STDOUT"
	TASK_OUTPUT_STDERR TaskOutputType = "STDERR"
)

type TaskOutput struct {
	TaskGUID uuid.UUID      `json:"taskGuid"`
	TaskMeta interface{}    `json:"taskMeta,omitempty"`
	Time     time.Time      `json:"time"`
	Type     TaskOutputType `json:"type"`
	Output   string         `json:"output"`
}

func (t TaskOutput) String() string {
	output := t.Output
	if len(output) > 100 {
		output = output[0:97] + "..."
	}
	return fmt.Sprintf(`TaskOutput[type:%s output:"%s"]`, t.Type, output)
}
