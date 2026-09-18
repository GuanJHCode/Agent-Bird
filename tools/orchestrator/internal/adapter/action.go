package adapter

// CandidateAction is an owner-declared step in the existing DAG. SourceTask is
// resolved only from the coordinator's accepted Host result, never raw prompt.
type CandidateAction struct {
	AutoDirectory bool            `json:"auto_directory,omitempty"`
	Version       int             `json:"version"`
	Operation     string          `json:"operation"`
	SourceTask    string          `json:"source_task"`
	Command       []string        `json:"command,omitempty"`
	Target        CandidateTarget `json:"target"`
}

type CandidateTarget struct {
	Worktree string `json:"worktree"`
	Ref      string `json:"ref"`
	BaseOID  string `json:"base_oid"`
}

func (i Invocation) CandidateAction() *CandidateAction {
	if i.action == nil {
		return nil
	}
	copy := *i.action
	copy.Command = append([]string(nil), i.action.Command...)
	return &copy
}

// Native schema validation reinforces the Host's strict review decoder. The
// schema is fixed by the adapter, never supplied by a task or model.
const candidateReviewSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["approve","reject"]},"summary":{"type":"string","minLength":1,"maxLength":65536}},"required":["decision","summary"],"additionalProperties":false}`

func usesCandidateReviewSchema(req Request) bool {
	return (req.Provider == ProviderClaude || req.Provider == ProviderAGY) && req.Profile != nil && req.Profile.Role == Reviewer && req.Action != nil && req.Action.Version == 1 && req.Action.Operation == "review"
}
