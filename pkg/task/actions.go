package task

type Action string

const (
	ActionCancel      Action = "cancel"
	ActionRetry       Action = "retry"
	ActionDismiss     Action = "dismiss"
	ActionOpenInput   Action = "open_input"
	ActionCommitInput Action = "commit_input"
	ActionOpenOutput  Action = "open_output"
)

func actionsForCapabilities(capabilities ItemCapabilities) []Action {
	actions := make([]Action, 0, 4)
	if capabilities.OpenInput {
		actions = append(actions, ActionOpenInput)
	}
	if capabilities.CommitInput {
		actions = append(actions, ActionCommitInput)
	}
	if capabilities.OpenOutput {
		actions = append(actions, ActionOpenOutput)
	}
	if capabilities.Cancelable {
		actions = append(actions, ActionCancel)
	}
	return actions
}

func ActionsForItemCapabilities(capabilities ItemCapabilities) []Action {
	return actionsForCapabilities(capabilities)
}
