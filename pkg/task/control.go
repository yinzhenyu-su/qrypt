package task

import "context"

// TaskController is the application-level mutation contract shared by local
// transports such as the CLI control server and the Mobile facade.
type TaskController interface {
	CancelTask(context.Context, string) error
	RetryTask(context.Context, string) error
}
