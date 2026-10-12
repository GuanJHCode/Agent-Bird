//go:build !windows

package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func cleanupWithRecovery(ctx context.Context, req CleanupRequest, receipt cleanupReceiptBinding, journal CleanupRecoveryJournal, out CleanupOutcome) (CleanupOutcome, error) {
	lock, err := acquireIntegrationLock(receipt.CommonDir)
	if err != nil {
		return out, err
	}
	defer releaseIntegrationLock(lock)
	state, err := journal.LoadCleanupRecovery(ctx, out.ReceiptSHA256)
	if err != nil {
		return out, err
	}
	if state.Found && state.Plan == nil {
		// Old pending operations have neither the inventory nor action intents
		// required to resume. A completed legacy result keeps its old semantics.
		if state.Outcome.State == CleanupRemoved {
			if err = cleanupAbsent(req.Root); err != nil {
				return out, err
			}
			if err = cleanupRefAbsent(ctx, receipt); err != nil {
				return out, err
			}
			return state.Outcome, nil
		}
		return out, nil
	}
	for _, pid := range req.ActivePIDs {
		if pid > 0 && syscall.Kill(pid, 0) != syscall.ESRCH {
			return out, errors.New("cleanup writer alive or unknown")
		}
	}
	if state.Plan == nil {
		if _, e := os.Lstat(req.Root); os.IsNotExist(e) {
			return out, nil
		}
		plan, e := prepareRecoveryPlan(ctx, req, receipt)
		if e != nil {
			return out, e
		}
		if e = journal.PersistCleanupPlan(ctx, plan); e != nil {
			return out, e
		}
		state = CleanupRecoveryState{Plan: &plan, Step: -1, Found: true, Intent: plan.Intent}
	}
	plan := *state.Plan
	if err = validateRecoveryPlan(req, receipt, plan); err != nil {
		return out, err
	}
	actions := cleanupActions(plan)
	if state.Step < -1 || state.Step >= len(actions) {
		return out, errors.New("cleanup checkpoint outside plan")
	}
	if state.Outcome.State == CleanupRemoved {
		if state.Step != len(actions)-1 {
			return out, errors.New("cleanup incomplete final checkpoint")
		}
		if err = verifyRecoveryFinished(ctx, receipt, plan); err != nil {
			return out, err
		}
		return state.Outcome, nil
	}
	if state.Step == -1 && req.BeforeRemove != nil {
		if err = req.BeforeRemove(); err != nil {
			return out, err
		}
	}
	for index := max(state.Step, 0); index < len(actions); index++ {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if err = verifyRecoveryEnvironment(plan, index); err != nil {
			return out, err
		}
		if index > state.Step {
			step := CleanupStep{ReceiptSHA256: plan.Intent.ReceiptSHA256, PlanSHA256: CleanupPlanDigest(plan), Index: index}
			if err = journal.PersistCleanupStep(ctx, step); err != nil {
				return out, err
			}
			state.Step = index
		}
		action := actions[index]
		if req.BeforeCleanupAction != nil {
			if err = req.BeforeCleanupAction(index, action.kind); err != nil {
				return out, err
			}
		}
		if action.kind == "ref" {
			err = DeletePrivateRef(ctx, receipt.RepoRoot, receipt.PrivateRef, receipt.PrivateRefExpectedOID)
		} else {
			err = executeCleanupAction(plan, action)
		}
		if err != nil {
			return out, err
		}
		if action.kind == "quarantine" && action.tree == 0 {
			quarantined := filepath.Join(filepath.Dir(plan.Root.Path), plan.Root.Quarantine, "root")
			if req.AfterQuarantine != nil {
				if err = req.AfterQuarantine(quarantined); err != nil {
					return out, err
				}
			}
			if req.BeforeAnchoredRemove != nil {
				if err = req.BeforeAnchoredRemove(quarantined); err != nil {
					return out, err
				}
			}
		}
		if req.AfterCleanupAction != nil {
			if err = req.AfterCleanupAction(index, action.kind); err != nil {
				return out, err
			}
		}
	}
	if err = verifyRecoveryFinished(ctx, receipt, plan); err != nil {
		return out, err
	}
	out.State, out.Code, out.RemovedFiles = CleanupRemoved, "", len(plan.Root.Files)
	if receipt.PrivateRef != "" {
		out.PrivateRefState = "deleted"
	}
	out.RecordedAt = time.Now().UTC()
	if err = req.Journal.RecordCleanup(ctx, out); err != nil {
		out.State = CleanupPending
		return out, err
	}
	return out, nil
}

func prepareRecoveryPlan(ctx context.Context, req CleanupRequest, receipt cleanupReceiptBinding) (CleanupPlan, error) {
	var p CleanupPlan
	p.Version = 1
	p.Intent = CleanupIntent{Root: receipt.WorktreeIdentity, AttemptID: receipt.AttemptID, CandidateOID: receipt.CommitOID, RootIdentity: receipt.WorktreeIdentity, PrivateRef: receipt.PrivateRef, PrivateRefExpectedOID: receipt.PrivateRefExpectedOID, ReceiptSHA256: receipt.hash(), CreatedAt: time.Now().UTC()}
	p.RepoRoot, p.CommonDir = receipt.RepoRoot, receipt.CommonDir
	var err error
	if p.RepoIdentity, err = directoryIdentity(p.RepoRoot); err != nil {
		return p, err
	}
	if p.CommonIdentity, err = directoryIdentity(p.CommonDir); err != nil {
		return p, err
	}
	if receipt.PrivateRef != "" {
		current, exists, e := privateRefOID(ctx, receipt.RepoRoot, receipt.PrivateRef)
		if e != nil {
			return p, e
		}
		if !exists || current != receipt.PrivateRefExpectedOID {
			return p, fail(CodeTargetDrift, "cleanup ref", errors.New("private ref changed"))
		}
	}
	admin, err := prepareCleanupAdmin(ctx, receipt.WorktreeIdentity, receipt.CommonDir)
	if err != nil {
		return p, err
	}
	defer admin.close()
	// Create only empty private metadata before plan admission. A crash here
	// cannot remove original data; an unregistered empty sink is never adopted.
	prepare := func(path string, files map[string]FileIdentity) (CleanupTreePlan, error) {
		tree := CleanupTreePlan{Path: path, Files: files}
		var e error
		if tree.Identity, e = directoryIdentity(path); e != nil {
			return tree, e
		}
		if tree.ParentIdentity, e = directoryIdentity(filepath.Dir(path)); e != nil {
			return tree, e
		}
		parent, e := openCleanupDirectory(filepath.Dir(path), tree.ParentIdentity)
		if e != nil {
			return tree, e
		}
		defer parent.Close()
		name, sink, e := makePrivateDirAt(int(parent.Fd()), ".g3-cleanup-")
		if e != nil {
			return tree, e
		}
		defer sink.Close()
		tree.Quarantine = name
		tree.StagePrefix = "entry-"
		st, e := sink.Stat()
		if e != nil {
			return tree, e
		}
		tree.SinkIdentity = cleanupDirectoryInfo(st)
		if e = sink.Sync(); e != nil {
			return tree, e
		}
		return tree, parent.Sync()
	}
	current, err := snapshot(receipt.WorktreeIdentity)
	if err != nil {
		return p, err
	}
	if !reflect.DeepEqual(current, receipt.Files) {
		return p, fail(CodeIdentityChanged, "cleanup inventory", errors.New("receipt inventory changed"))
	}
	if p.Root, err = prepare(receipt.WorktreeIdentity, current); err != nil {
		return p, err
	}
	if p.Admin, err = prepare(admin.path, admin.files); err != nil {
		return p, err
	}
	return p, validateRecoveryPlan(req, receipt, p)
}

func validateRecoveryPlan(req CleanupRequest, r cleanupReceiptBinding, p CleanupPlan) error {
	invalid := func() error {
		return fail(CodeIdentityChanged, "cleanup plan", errors.New("cleanup plan binding changed"))
	}
	if p.Version != 1 || p.Intent.ReceiptSHA256 != r.hash() || p.Intent.Root != r.WorktreeIdentity || p.Intent.RootIdentity != r.WorktreeIdentity || p.Intent.AttemptID != r.AttemptID || p.Intent.CandidateOID != r.CommitOID || p.Intent.PrivateRef != r.PrivateRef || p.Intent.PrivateRefExpectedOID != r.PrivateRefExpectedOID || p.RepoRoot != r.RepoRoot || p.CommonDir != r.CommonDir || p.Root.Path != r.WorktreeIdentity || CanonicalPath(req.Root) != r.WorktreeIdentity || !reflect.DeepEqual(p.Root.Files, r.Files) || p.Root.Identity != r.RootIdentity {
		return invalid()
	}
	if filepath.Dir(p.Admin.Path) != filepath.Join(r.CommonDir, "worktrees") || p.Admin.Path == p.Root.Path || p.Root.Path == p.RepoRoot || p.Root.Path == p.CommonDir {
		return invalid()
	}
	if req.CandidateReceipt != nil && (p.RepoIdentity != req.CandidateReceipt.RepoRootIdentity || p.CommonIdentity != req.CandidateReceipt.CommonDirIdentity) {
		return invalid()
	}
	for _, tree := range []CleanupTreePlan{p.Root, p.Admin} {
		if !filepath.IsAbs(tree.Path) || filepath.Clean(tree.Path) != tree.Path || tree.StagePrefix != "entry-" || !strings.HasPrefix(tree.Quarantine, ".g3-cleanup-") || len(tree.Quarantine) != len(".g3-cleanup-")+16 || filepath.Base(tree.Quarantine) != tree.Quarantine || tree.SinkIdentity.UID != int64(os.Geteuid()) || os.FileMode(tree.SinkIdentity.Mode) != os.ModeDir|0700 || tree.SinkIdentity.Inode == 0 || tree.ParentIdentity.Inode == 0 || tree.Identity.Inode == 0 || len(tree.Files) == 0 {
			return invalid()
		}
		for name, id := range tree.Files {
			if name == "." || name == ".." || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") || id.Inode == 0 || (!os.FileMode(id.Mode).IsRegular() && !os.FileMode(id.Mode).IsDir()) {
				return invalid()
			}
		}
	}
	return nil
}

type cleanupAction struct {
	kind string
	tree int
	path string
	slot int
}

func cleanupActions(plan CleanupPlan) []cleanupAction {
	actions := make([]cleanupAction, 0, CleanupPlanStepCount(plan))
	for n, tree := range []CleanupTreePlan{plan.Root, plan.Admin} {
		actions = append(actions, cleanupAction{kind: "quarantine", tree: n})
		paths := make([]string, 0, len(tree.Files))
		for path := range tree.Files {
			paths = append(paths, path)
		}
		sortRemovalPaths(paths)
		for slot, path := range paths {
			actions = append(actions, cleanupAction{kind: "move", tree: n, path: path, slot: slot}, cleanupAction{kind: "unlink", tree: n, path: path, slot: slot})
		}
		actions = append(actions, cleanupAction{kind: "root_unlink", tree: n}, cleanupAction{kind: "sink_unlink", tree: n})
	}
	if plan.Intent.PrivateRef != "" {
		actions = append(actions, cleanupAction{kind: "ref"})
	}
	return actions
}

func cleanupDirectoryInfo(info os.FileInfo) DirectoryIdentity {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return DirectoryIdentity{}
	}
	return DirectoryIdentity{Device: int64(stat.Dev), Inode: int64(stat.Ino), Mode: int64(info.Mode()), UID: int64(stat.Uid)}
}
func openCleanupDirectory(path string, expected DirectoryIdentity) (*os.File, error) {
	if err := noSymlinkComponents(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || cleanupDirectoryInfo(info) != expected {
		f.Close()
		return nil, fail(CodeIdentityChanged, "cleanup directory", errors.New("directory identity changed"))
	}
	return f, nil
}
func cleanupAbsent(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fail(CodeIdentityChanged, "cleanup replay", errors.New("removed path recreated"))
}
func cleanupRefAbsent(ctx context.Context, r cleanupReceiptBinding) error {
	if r.PrivateRef == "" {
		return nil
	}
	_, exists, err := privateRefOID(ctx, r.RepoRoot, r.PrivateRef)
	if err != nil {
		return err
	}
	if exists {
		return fail(CodeTargetDrift, "cleanup replay", errors.New("private ref recreated"))
	}
	return nil
}
func verifyRecoveryEnvironment(p CleanupPlan, index int) error {
	for path, id := range map[string]DirectoryIdentity{p.RepoRoot: p.RepoIdentity, p.CommonDir: p.CommonIdentity, filepath.Dir(p.Root.Path): p.Root.ParentIdentity, filepath.Dir(p.Admin.Path): p.Admin.ParentIdentity} {
		f, err := openCleanupDirectory(path, id)
		if err != nil {
			return err
		}
		f.Close()
	}
	start := 0
	for _, tree := range []CleanupTreePlan{p.Root, p.Admin} {
		if index > start {
			if err := cleanupAbsent(tree.Path); err != nil {
				return err
			}
		}
		count := 3 + 2*len(tree.Files)
		if index >= start+count {
			if err := cleanupAbsent(filepath.Join(filepath.Dir(tree.Path), tree.Quarantine)); err != nil {
				return err
			}
		}
		start += count
	}
	return nil
}
func verifyRecoveryFinished(ctx context.Context, r cleanupReceiptBinding, p CleanupPlan) error {
	if err := verifyRecoveryEnvironment(p, CleanupPlanStepCount(p)); err != nil {
		return err
	}
	return cleanupRefAbsent(ctx, r)
}

func cleanupAt(parent *os.File, name string) (os.FileInfo, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("invalid cleanup component")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	return f.Stat()
}
func verifyCleanupEntry(parent *os.File, name string, expected FileIdentity) error {
	st, err := cleanupAt(parent, name)
	if err != nil {
		return err
	}
	id, err := identityFromInfo(st)
	if err != nil || !sameRemovalIdentity(id, expected) {
		return fail(CodeIdentityChanged, "cleanup entry", errors.New("entry identity changed"))
	}
	return nil
}
func verifyCleanupDir(parent *os.File, name string, expected DirectoryIdentity) error {
	st, err := cleanupAt(parent, name)
	if err != nil {
		return err
	}
	if cleanupDirectoryInfo(st) != expected {
		return fail(CodeIdentityChanged, "cleanup directory", errors.New("directory identity changed"))
	}
	return nil
}
func anchoredAbsent(parent *os.File, name string) (bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	return false, err
}
func syncCleanupParents(a, b *os.File) error {
	if err := a.Sync(); err != nil {
		return err
	}
	if a != b {
		return b.Sync()
	}
	return nil
}

func executeCleanupAction(plan CleanupPlan, action cleanupAction) error {
	tree := plan.Root
	if action.tree == 1 {
		tree = plan.Admin
	}
	parent, err := openCleanupDirectory(filepath.Dir(tree.Path), tree.ParentIdentity)
	if err != nil {
		return err
	}
	defer parent.Close()
	sinkAbsent, err := anchoredAbsent(parent, tree.Quarantine)
	if err != nil {
		return err
	}
	if sinkAbsent {
		if action.kind == "sink_unlink" {
			return parent.Sync()
		}
		return fail(CodeIdentityChanged, "cleanup sink", errors.New("private sink missing before removal intent"))
	}
	if err = verifyCleanupDir(parent, tree.Quarantine, tree.SinkIdentity); err != nil {
		return err
	}
	sink, err := openAtFile(parent, tree.Quarantine, true)
	if err != nil {
		return err
	}
	defer sink.Close()
	if action.kind == "sink_unlink" {
		if err = unix.Unlinkat(int(parent.Fd()), tree.Quarantine, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		return parent.Sync()
	}
	if action.kind == "quarantine" {
		sourceAbsent, e := anchoredAbsent(parent, filepath.Base(tree.Path))
		if e != nil {
			return e
		}
		targetAbsent, e := anchoredAbsent(sink, "root")
		if e != nil {
			return e
		}
		if sourceAbsent == targetAbsent {
			return fail(CodeIdentityChanged, "cleanup quarantine", errors.New("ambiguous root location"))
		}
		if sourceAbsent {
			if e = verifyCleanupDir(sink, "root", tree.Identity); e != nil {
				return e
			}
			return syncCleanupParents(parent, sink)
		}
		if e = verifyCleanupDir(parent, filepath.Base(tree.Path), tree.Identity); e != nil {
			return e
		}
		if e = cleanupRenameExclusive(parent, filepath.Base(tree.Path), sink, "root"); e != nil {
			return e
		}
		if e = syncCleanupParents(parent, sink); e != nil {
			return e
		}
		return verifyCleanupDir(sink, "root", tree.Identity)
	}
	if action.kind == "root_unlink" {
		absent, e := anchoredAbsent(sink, "root")
		if e != nil {
			return e
		}
		if absent {
			return sink.Sync()
		}
		if e = verifyCleanupDir(sink, "root", tree.Identity); e != nil {
			return e
		}
		if e = unix.Unlinkat(int(sink.Fd()), "root", unix.AT_REMOVEDIR); e != nil {
			return e
		}
		return sink.Sync()
	}
	if err = verifyCleanupDir(sink, "root", tree.Identity); err != nil {
		return err
	}
	root, err := openAtFile(sink, "root", true)
	if err != nil {
		return err
	}
	defer root.Close()
	stage := fmt.Sprintf("%s%08d", tree.StagePrefix, action.slot)
	expected := tree.Files[action.path]
	if action.kind == "unlink" {
		absent, e := anchoredAbsent(sink, stage)
		if e != nil {
			return e
		}
		if absent {
			return sink.Sync()
		}
		if e = verifyCleanupEntry(sink, stage, expected); e != nil {
			return e
		}
		flags := 0
		if os.FileMode(expected.Mode).IsDir() {
			flags = unix.AT_REMOVEDIR
		}
		if e = unix.Unlinkat(int(sink.Fd()), stage, flags); e != nil {
			return e
		}
		return sink.Sync()
	}
	source, name, closeSource, err := openRelativeParent(root, action.path)
	if err != nil {
		return err
	}
	defer closeSource()
	sourceAbsent, err := anchoredAbsent(source, name)
	if err != nil {
		return err
	}
	targetAbsent, err := anchoredAbsent(sink, stage)
	if err != nil {
		return err
	}
	if sourceAbsent == targetAbsent {
		return fail(CodeIdentityChanged, "cleanup move", errors.New("ambiguous entry location"))
	}
	if sourceAbsent {
		if err = verifyCleanupEntry(sink, stage, expected); err != nil {
			return err
		}
		return syncCleanupParents(source, sink)
	}
	// Move before verification. A late substitution is retained in the private
	// sink; no unlink intent will be issued for an unverified object.
	if err = cleanupRenameExclusive(source, name, sink, stage); err != nil {
		return err
	}
	if err = syncCleanupParents(source, sink); err != nil {
		return err
	}
	return verifyCleanupEntry(sink, stage, expected)
}
