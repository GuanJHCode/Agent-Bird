package codexconfig

import (
	"errors"
	"slices"
	"testing"
)

func TestGoalsManagedControlIsExplicit(t *testing.T) {
	p := mustProject(t, currentSource, ReadOnly)
	if !slices.Contains(p.ManagedOverrides(), "features.goals=false") {
		t.Fatal("pinned stable/default-enabled Goals lacks an explicit managed control")
	}
}

func TestGoalsEffectiveRequiresExplicitFalse(t *testing.T) {
	for _, mode := range []string{"false", "true", "missing", "null", "string"} {
		t.Run(mode, func(t *testing.T) {
			p := mustProject(t, currentSource, ReadOnly)
			config := compatibilityEffective(ReadOnly, true)
			features := config["features"].(map[string]any)
			switch mode {
			case "false":
				features["goals"] = false
			case "true":
				features["goals"] = true
			case "missing":
				delete(features, "goals")
			case "null":
				features["goals"] = nil
			case "string":
				features["goals"] = "false"
			}
			err := p.VerifyEffective(encodeEffective(t, config))
			if mode == "false" && err != nil || mode != "false" && !errors.Is(err, ErrEffectivePolicy) {
				t.Fatalf("explicit Goals control %s: %v", mode, err)
			}
		})
	}
}
