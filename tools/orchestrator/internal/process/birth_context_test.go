package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBirthContextCancellationStopsObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := birthContext(ctx, 42, func(ctx context.Context, pid int) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("birth observation exceeded deadline")
	}
}
