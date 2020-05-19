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
	GUID       string      `json:"guid"`
	Status     TaskStatus  `json:"status"`
	Meta       interface{} `json:"meta"`
	StartedAt  *time.Time  `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt"`
	ExitCode   int         `json:"exitCode"`
	Error      error       `json:"error"`
	Executed   bool        `json:"executed"`
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
	TaskGUID uuid.UUID
	TaskMeta interface{}
	Time     time.Time
	Type     TaskOutputType
	Output   string
}

func (t TaskOutput) String() string {
	output := t.Output
	if len(output) > 100 {
		output = output[0:97] + "..."
	}
	return fmt.Sprintf(`TaskOutput[type:%s output:"%s"]`, t.Type, output)
}
