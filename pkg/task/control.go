package task

import "context"

// Controller is the application-level task mutation contract shared by local
// transports such as the CLI control server and the Mobile facade.
type Controller interface {
	CancelTask(context.Context, string) error
	RetryTask(context.Context, string) error
}
