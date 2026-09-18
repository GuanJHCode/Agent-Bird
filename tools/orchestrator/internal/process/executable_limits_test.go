package process

import (
	"context"
	"errors"
	"testing"
)

func TestStartRejectsNonRegularExecutableBeforeHashing(t *testing.T) {
	_, err := Start(context.Background(), Command{Path: "/dev/null"})
	if !errors.Is(err, CodeError("command_executable_invalid")) {
		t.Fatalf("non-regular file reached hashing/execution: %v", err)
	}
}
