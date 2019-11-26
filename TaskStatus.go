package utaskscheduler

type TaskStatus string

const (
	TASK_STATUS_SCHEDULED   TaskStatus = "SCHEDULED"
	TASK_STATUS_IN_PROGRESS TaskStatus = "IN_PROGRESS"
	TASK_STATUS_SUCCESS     TaskStatus = "SUCCESS"
	TASK_STATUS_FAILED      TaskStatus = "FAILED"
)
