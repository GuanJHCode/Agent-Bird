package host

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"golang.org/x/sys/unix"
)

var codexSQLiteFamilies = [...]string{"state_5", "logs_2", "goals_1", "memories_1", "queue_1", "thread_history_1"}

type codexPersistentSlot struct {
	Name string         `json:"name"`
	File codexStateFile `json:"file"`
}

// Only a physical byte component. Its caller separately validates the closed
// layout, auxiliary aggregate, protected/source references and original exit.
// No SQLite API, process observation or publication occurs in this helper.
func codexCapturePersistentSlots(ctx context.Context, home, sqlite codexStateRoot, binding codexsession.ThreadBinding, installationID string, control codexStateReadControl) ([]codexPersistentSlot, error) {
	if filepath.Base(home.Path) != "home" || filepath.Base(sqlite.Path) != "sqlite" || filepath.Dir(home.Path) != filepath.Dir(sqlite.Path) || !codexCanonicalUUID.MatchString(installationID) {
		return nil, errCodexStateFile
	}
	relative, err := codexManagedRolloutRelative(home.Path, binding)
	if err != nil {
		return nil, errCodexStateFile
	}
	var slots []codexPersistentSlot
	parents := map[string]codexStateDirectory{}
	mergeParents := func(observation codexStateObservation) error {
		for _, parent := range observation.Parents {
			if previous, ok := parents[parent.Path]; ok && previous != parent {
				return errCodexStateFile
			}
			parents[parent.Path] = parent
			if len(parents) > 32 {
				return errCodexStateFile
			}
		}
		return nil
	}
	for _, family := range codexSQLiteFamilies {
		for i, suffix := range []string{"", "-wal", "-shm"} {
			name := family + ".sqlite" + suffix
			observation, err := codexReadStateFile(ctx, sqlite, name, control)
			if err != nil || i == 0 && (!observation.File.Present || observation.File.Identity.Size < 100 || !bytes.Equal(observation.Head[:16], []byte("SQLite format 3\x00"))) || mergeParents(observation) != nil {
				return nil, errCodexStateFile
			}
			slots = append(slots, codexPersistentSlot{Name: "sqlite/" + name, File: observation.File})
		}
	}
	rolloutName := filepath.Join("sessions", relative)
	rollout, err := codexReadStateFile(ctx, home, rolloutName, control)
	if err != nil || !rollout.File.Present || rollout.File.Identity.Size == 0 || rollout.Tail != '\n' || mergeParents(rollout) != nil {
		return nil, errCodexStateFile
	}
	slots = append(slots, codexPersistentSlot{Name: "home/" + rolloutName, File: rollout.File})
	installationControl := control
	if installationControl.FileLimit == 0 || installationControl.FileLimit > 37 {
		installationControl.FileLimit = 37
	}
	installation, err := codexReadStateFile(ctx, home, "installation_id", installationControl)
	if err != nil || !installation.File.Present || (installation.File.Identity.Size != 36 && installation.File.Identity.Size != 37) || mergeParents(installation) != nil {
		return nil, errCodexStateFile
	}
	value := installation.Head[:installation.HeadBytes]
	if len(value) == 37 {
		if value[36] != '\n' {
			return nil, errCodexStateFile
		}
		value = value[:36]
	}
	if string(value) != installationID {
		return nil, errCodexStateFile
	}
	slots = append(slots, codexPersistentSlot{Name: "home/installation_id", File: installation.File})
	// Recheck earlier files and every original parent edge after the final
	// read. This is metadata-only and does not consume another byte budget.
	for _, slot := range slots {
		root, relative := home, strings.TrimPrefix(slot.Name, "home/")
		if strings.HasPrefix(slot.Name, "sqlite/") {
			root, relative = sqlite, strings.TrimPrefix(slot.Name, "sqlite/")
		}
		if codexCheckPersistentSlotMetadata(ctx, root, relative, slot.File, parents) != nil {
			return nil, errCodexStateFile
		}
	}
	return slots, nil
}

func codexCheckPersistentSlotMetadata(ctx context.Context, root codexStateRoot, relative string, expected codexStateFile, parents map[string]codexStateDirectory) error {
	chain, err := codexOpenStateRoot(ctx, root.Path)
	if err != nil {
		return err
	}
	defer func() { chain.close() }()
	if len(chain) != len(root.Parents) {
		return errCodexStateFile
	}
	for i, node := range chain {
		if node.identity != root.Parents[i] {
			return errCodexStateFile
		}
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, name := range parts[:len(parts)-1] {
		if codexAppendStateDirectory(ctx, &chain, name, true) != nil {
			return errCodexStateFile
		}
	}
	for _, node := range chain {
		if previous, ok := parents[node.identity.Path]; !ok || previous != node.identity {
			return errCodexStateFile
		}
	}
	var stat unix.Stat_t
	err = unix.Fstatat(chain[len(chain)-1].fd, parts[len(parts)-1], &stat, unix.AT_SYMLINK_NOFOLLOW)
	if expected.Present {
		if err != nil || !codexSafeOwnedFile(stat) || codexFileFromStat(stat) != expected.Identity {
			return errCodexStateFile
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return errCodexStateFile
	}
	return chain.verify(ctx)
}

func codexValidatePersistentSlots(ctx context.Context, home, sqlite codexStateRoot, binding codexsession.ThreadBinding, installationID string, previous []codexPersistentSlot, control codexStateReadControl) error {
	if len(previous) != 20 {
		return errCodexStateFile
	}
	current, err := codexCapturePersistentSlots(ctx, home, sqlite, binding, installationID, control)
	if err != nil || !reflect.DeepEqual(previous, current) {
		return errCodexStateFile
	}
	return nil
}
