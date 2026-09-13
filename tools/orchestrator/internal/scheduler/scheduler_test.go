package scheduler

import (
	"context"
	"testing"
)

func TestQueueUsesFIFOAndTwoSlots(t *testing.T) {
	s := New(2)
	s.Add(Task{ID: "a"})
	s.Add(Task{ID: "b"})
	s.Add(Task{ID: "c"})
	first, err := s.Next(context.Background())
	if err != nil || first.ID != "a" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := s.Next(context.Background())
	if err != nil || second.ID != "b" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if _, err := s.Next(context.Background()); err == nil {
		t.Fatal("expected slot limit")
	}
	s.Complete("a")
	third, err := s.Next(context.Background())
	if err != nil || third.ID != "c" {
		t.Fatalf("third=%#v err=%v", third, err)
	}
}
