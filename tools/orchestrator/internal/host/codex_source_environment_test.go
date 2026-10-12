//go:build darwin

package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const codexEnvironmentFailure = "codex_source_auth_environment_unsupported"

var codexEnvironmentKeys = []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"}

func codexExpectEnvironmentFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != codexEnvironmentFailure {
		// Do not serialize even synthetic environment values or other errors.
		t.Fatal("missing fixed source environment rejection")
	}
}

// The source config is deliberately not readable as a regular file. The fixed
// environment error must precede source-reference/projector validation and any
// creation of attempt-owned state. All files are synthetic and task-owned.
func TestCodexSourceEnvironmentInitialRejectsBeforeSourceRead(t *testing.T) {
	for _, key := range codexEnvironmentKeys {
		for _, value := range []string{"SYNTHETIC_ENV_NOT_EXPORTED", " "} {
			name := key + map[bool]string{true: "_whitespace", false: "_nonempty"}[value == " "]
			t.Run(name, func(t *testing.T) {
				h, g, inv, env := codexOriginFixture(t)
				source, err := codexSourceHome(env)
				if err != nil || os.Remove(filepath.Join(source, "config.toml")) != nil || os.Mkdir(filepath.Join(source, "config.toml"), 0700) != nil {
					t.Fatal("source boundary fixture")
				}
				env = append(env, key+"="+value)
				before := append([]string(nil), env...)
				_, err = h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
				codexExpectEnvironmentFailure(t, err)
				if !reflect.DeepEqual(env, before) {
					t.Fatal("source environment was silently changed")
				}
				if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, "codex-state")); !os.IsNotExist(err) {
					t.Fatal("unsupported environment created owned state")
				}
			})
		}
	}
}

func TestCodexSourceEnvironmentRestoreRejectsBeforeOwnedConfigRead(t *testing.T) {
	for _, dispatch := range []bool{false, true} {
		for _, key := range codexEnvironmentKeys {
			t.Run(map[bool]string{false: "direct", true: "dispatch"}[dispatch]+"/"+key, func(t *testing.T) {
				h, g, inv, env := codexHardlinkFixture(t, true)
				config := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state", "home", "config.toml")
				if os.Remove(config) != nil || os.Mkdir(config, 0700) != nil {
					t.Fatal("owned config read boundary fixture")
				}
				env = append(env, key+"=SYNTHETIC_ENV_NOT_EXPORTED")
				var err error
				if dispatch {
					_, err = h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
				} else {
					_, err = h.restoreManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
				}
				codexExpectEnvironmentFailure(t, err)
				if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-origin.json")); !os.IsNotExist(err) {
					t.Fatal("environment refusal published continuation state")
				}
			})
		}
	}
}

// This genuinely enters private ExecuteLaunch, but the exclusively owned
// provider pin is missing. The production launcher's pin check precedes
// cmd.Start, so neither the old nor new path can spawn a child. It proves
// admission/never-started routing, not physical Wait/tree exit.
func TestCodexSourceEnvironmentExecuteRejectsBeforeStart(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, key := range codexEnvironmentKeys {
			t.Run(map[bool]string{false: "initial", true: "restore"}[resume]+"/"+key, func(t *testing.T) {
				h, g, inv, env := codexHardlinkFixture(t, resume)
				codexHardlinkEnvironment(t, env)
				t.Setenv(key, "SYNTHETIC_ENV_NOT_EXPORTED")
				g.ReservationID, g.GrantedActiveMS = "environment-reservation", 60000
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				reached := false
				result, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{beforeStart: func() { reached = true }})
				codexExpectEnvironmentFailure(t, err)
				spool, spoolErr := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
				if spoolErr != nil {
					t.Fatal("owned spool")
				}
				events, readErr := spool.Read()
				if reached || readErr != nil || result.Status != "failed" || len(events) != 2 || events[0].Kind != contract.EventPrepared || events[1].Kind != contract.EventLaunchRejected || events[1].Process != nil || events[1].ActiveMS != 0 || len(h.codexLeases) != 0 || len(h.active) != 0 {
					t.Fatal("environment reached Start or retained/fabricated physical ownership")
				}
				if !resume {
					if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, "codex-state")); !os.IsNotExist(err) {
						t.Fatal("actual initial consumer created state before refusal")
					}
				}
			})
		}
	}
}

func TestCodexSourceEnvironmentBuilderRejectsPreparedDrift(t *testing.T) {
	for _, key := range codexEnvironmentKeys {
		t.Run(key, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal("admitted preparation")
			}
			env = append(env, key+"=SYNTHETIC_ENV_NOT_EXPORTED")
			before := append([]string(nil), env...)
			cmd, _, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
			codexExpectEnvironmentFailure(t, err)
			if cmd.Path != "" || !reflect.DeepEqual(env, before) {
				t.Fatal("builder introduced or cleared source override")
			}
			_, err = codexOwnedCommandEnvironment(env, p)
			codexExpectEnvironmentFailure(t, err)
		})
	}
}

func TestCodexSourceEnvironmentAbsentAndEmptyPreserveOtherEnvironment(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			t.Run(map[bool]string{false: "initial", true: "restore"}[resume]+"/"+map[bool]string{false: "absent", true: "empty"}[empty], func(t *testing.T) {
				h, g, inv, env := codexHardlinkFixture(t, resume)
				if empty {
					for _, key := range codexEnvironmentKeys {
						env = append(env, key+"=")
					}
				}
				env = append(env, "C06_ORDINARY_ENV=SYNTHETIC_OTHER_ENV", "PATH=/synthetic/bin")
				before := append([]string(nil), env...)
				p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
				if err != nil {
					t.Fatal("valid absent/empty environment rejected")
				}
				cmd, _, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
				if err != nil || !reflect.DeepEqual(env, before) || cmd.PinnedPath != p.Origin.Lock.Binary.Path || cmd.Dir != inv.WorkingDirectory() {
					t.Fatal("valid source environment/defaults drifted")
				}
				values := map[string]string{}
				for _, pair := range cmd.Env {
					key, value, _ := strings.Cut(pair, "=")
					values[key] = value
				}
				if values["C06_ORDINARY_ENV"] != "SYNTHETIC_OTHER_ENV" || values["PATH"] != "/synthetic/bin" {
					t.Fatal("unrelated environment was changed")
				}
				for _, key := range codexEnvironmentKeys {
					value, present := values[key]
					if value != "" || present != empty {
						t.Fatal("empty/missing source override was rewritten")
					}
				}
			})
		}
	}
}
