package events

// Directory identifies this Spool instance for its original Host's anchored
// callers. The string is not filesystem identity or authority to reopen a spool.
func (s *Spool) Directory() string {
	if s == nil {
		return ""
	}
	return s.dir
}
