package host

import "errors"

type ownerCommandAction uint8

const (
	ownerCommandOK ownerCommandAction = iota
	ownerCommandInvalid
	ownerCommandStale
	ownerCommandStopped
	ownerCommandLocalFailure
	ownerCommandRecover
	ownerCommandTerminal
)

type ownerCommandRefusal struct {
	action ownerCommandAction
	cause  error
}

func (e *ownerCommandRefusal) Error() string { return e.cause.Error() }
func (e *ownerCommandRefusal) Unwrap() error { return e.cause }
func ownerCommandDisposition(err error) ownerCommandAction {
	if err == nil {
		return ownerCommandOK
	}
	var refusal *ownerCommandRefusal
	if errors.As(err, &refusal) {
		return refusal.action
	}
	var terminal sourceTerminalDisposition
	if errors.As(err, &terminal) {
		return ownerCommandTerminal
	}
	if ownerExecutionNeedsRecovery(err) {
		return ownerCommandRecover
	}
	return ownerCommandLocalFailure
}

func rejectOwnerCommand(action ownerCommandAction, cause error) error {
	return &ownerCommandRefusal{action: action, cause: cause}
}
