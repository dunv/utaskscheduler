package utaskscheduler

import (
	"fmt"
	"time"

	"github.com/google/uuid"
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
