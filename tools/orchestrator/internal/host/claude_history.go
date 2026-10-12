package host

import (
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type continuationFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
}
type claudeHistoryBinding struct {
	Version          int                      `json:"version"`
	Origin           continuationOrigin       `json:"origin"`
	ConfigRoot       string                   `json:"config_root"`
	ConfigIdentity   continuationFileIdentity `json:"config_identity"`
	ProjectsIdentity continuationFileIdentity `json:"projects_identity"`
	ProjectName      string                   `json:"project_name"`
	ProjectPath      string                   `json:"project_path"`
	ProjectIdentity  continuationFileIdentity `json:"project_identity"`
}
type claudeTranscriptBinding struct {
	Version   int                      `json:"version"`
	SessionID string                   `json:"session_id"`
	Path      string                   `json:"path"`
	Identity  continuationFileIdentity `json:"identity"`
}

func continuationOwnedDirectory(path string, permission os.FileMode) error {
	_, err := continuationDirectoryIdentity(path, permission)
	return err
}
func continuationDirectoryIdentity(path string, permission os.FileMode) (continuationFileIdentity, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return continuationFileIdentity{}, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ok || owner.Uid != uint32(os.Geteuid()) || st.Mode().Perm()&0022 != 0 || (permission != 0 && st.Mode().Perm() != permission) {
		return continuationFileIdentity{}, errors.New("continuation_directory_untrusted")
	}
	return continuationFileIdentity{uint64(owner.Dev), owner.Ino, owner.Uid, uint32(st.Mode().Perm())}, nil
}

func commandEnvironment(cmd process.Command, key string) string {
	value := os.Getenv(key)
	if cmd.ExactEnv {
		value = ""
	}
	for _, entry := range cmd.Env {
		if v, ok := strings.CutPrefix(entry, key+"="); ok {
			value = v
		}
	}
	return value
}
func setCommandEnvironment(cmd *process.Command, key, value string) {
	var env []string
	for _, entry := range cmd.Env {
		if !strings.HasPrefix(entry, key+"=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, key+"="+value)
}

func (h *Host) originalContinuationSegment(g contract.LaunchCommand) (string, error) {
	if g.Continuation == nil {
		return g.SegmentID, nil
	}
	entries, err := h.spoolEntries()
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		records, err := entry.spool.Read()
		if err != nil {
			return "", err
		}
		for _, e := range records {
			if e.EventID == g.Continuation.SessionEventID && e.Kind == contract.EventSession && e.RunID == g.RunID && e.TaskID == g.TaskID && e.AttemptID == g.AttemptID && e.WorkRevision == g.WorkRevision && e.ProducerID == h.producerID {
				return e.SegmentID, nil
			}
		}
	}
	return "", errors.New("resume_session_unbound")
}

func (h *Host) prepareClaudeHistory(cmd process.Command, g contract.LaunchCommand, pin adapter.BinaryPin) (process.Command, string, error) {
	if !adapter.ClaudeHistorySupported(pin) {
		if g.Continuation != nil {
			return cmd, "", errors.New("claude_session_history_not_verified")
		}
		return cmd, "", nil
	}
	skip := commandEnvironment(cmd, "CLAUDE_CODE_SKIP_PROMPT_HISTORY")
	if skip != "" && skip != "0" {
		if g.Continuation == nil {
			return cmd, "", nil
		}
		if skip == "1" {
			return cmd, "", errors.New("claude_session_history_disabled")
		}
		return cmd, "", errors.New("claude_session_history_config_unverified")
	}
	config := commandEnvironment(cmd, "CLAUDE_CONFIG_DIR")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cmd, "", err
		}
		config = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(config) || filepath.Clean(config) != config {
		return cmd, "", errors.New("claude_config_root_untrusted")
	}
	configID, err := continuationDirectoryIdentity(config, 0)
	if err != nil {
		return cmd, "", err
	}
	source, err := h.originalContinuationSegment(g)
	if err != nil {
		return cmd, "", err
	}
	sourceGrant := g
	sourceGrant.SegmentID = source
	path := filepath.Join(h.spoolRoot, g.AttemptID, source, "provider-history.json")
	projectName := "agent-bird-" + hashText(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + strconv.Itoa(g.WorkRevision))[:48]
	projects := filepath.Join(config, "projects")
	project := filepath.Join(projects, projectName)
	var bound claudeHistoryBinding
	err = readContinuationMetadata(path, &bound)
	if errors.Is(err, os.ErrNotExist) && g.Continuation == nil {
		if err = os.Mkdir(projects, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return cmd, "", err
		}
		projectsID, err := continuationDirectoryIdentity(projects, 0)
		if err != nil {
			return cmd, "", err
		}
		if err = os.Mkdir(project, 0700); err != nil {
			return cmd, "", errors.New("claude_project_directory_reused")
		}
		projectID, err := continuationDirectoryIdentity(project, 0700)
		if err != nil {
			return cmd, "", err
		}
		bound = claudeHistoryBinding{Version: 1, Origin: h.originForGrant(sourceGrant, "claude-code"), ConfigRoot: config, ConfigIdentity: configID, ProjectsIdentity: projectsID, ProjectName: projectName, ProjectPath: project, ProjectIdentity: projectID}
		spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: source})
		if err != nil {
			return cmd, "", err
		}
		if err = spool.WriteMetadata("provider-history.json", bound); err != nil {
			return cmd, "", err
		}
	} else if err != nil {
		return cmd, "", err
	}
	projectsID, err := continuationDirectoryIdentity(projects, 0)
	if err != nil {
		return cmd, "", err
	}
	projectID, err := continuationDirectoryIdentity(project, 0700)
	if err != nil {
		return cmd, "", err
	}
	if bound.Version != 1 || bound.Origin != h.originForGrant(sourceGrant, "claude-code") || bound.ConfigRoot != config || bound.ConfigIdentity != configID || bound.ProjectsIdentity != projectsID || bound.ProjectName != projectName || bound.ProjectPath != project || bound.ProjectIdentity != projectID {
		return cmd, "", errors.New("claude_history_binding_changed")
	}
	if g.Continuation != nil {
		if err = h.validateClaudeTranscript(g, source, project); err != nil {
			return cmd, "", err
		}
	}
	setCommandEnvironment(&cmd, "CLAUDE_CONFIG_DIR", config)
	setCommandEnvironment(&cmd, "CLAUDE_CODE_PROJECT_DIR_NAME", projectName)
	return cmd, project, nil
}

func (h *Host) validateClaudeTranscript(g contract.LaunchCommand, source, project string) error {
	if g.SessionID == "" || len(g.SessionID) > 256 || strings.ContainsAny(g.SessionID, "/\\\x00\r\n") {
		return errors.New("resume_session_invalid")
	}
	transcript := filepath.Join(project, g.SessionID+".jsonl")
	st, err := os.Lstat(transcript)
	if err != nil {
		return errors.New("claude_session_history_missing")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || st.Size() < 1 || st.Size() > 16*1024*1024 || !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 {
		return errors.New("claude_session_history_untrusted")
	}
	identity := continuationFileIdentity{uint64(owner.Dev), owner.Ino, owner.Uid, uint32(st.Mode().Perm())}
	bound := claudeTranscriptBinding{Version: 1, SessionID: g.SessionID, Path: transcript, Identity: identity}
	path := filepath.Join(h.spoolRoot, g.AttemptID, source, "provider-session.json")
	var old claudeTranscriptBinding
	if err = readContinuationMetadata(path, &old); err == nil {
		if old != bound {
			return errors.New("claude_session_history_changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: source})
	if err != nil {
		return err
	}
	return spool.WriteMetadata("provider-session.json", bound)
}
