package process

// ConfirmGroupExited observes the OS group without signalling it. An exited
// leader does not establish that its descendants have terminated.
func ConfirmGroupExited(pgid int) error {
	alive, err := processGroupAlive(pgid)
	if err != nil || alive {
		return ErrProcessTreeUnknown
	}
	return nil
}
