package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRefreshedSessionsRemainRevocable(t *testing.T) {
	rdb, mr := testRedis(t)
	s := NewSessions(rdb, time.Hour)
	ctx := context.Background()
	token, err := s.Create(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		mr.FastForward(40 * time.Minute)
		if _, err := s.Lookup(ctx, token); err != nil {
			t.Fatalf("active session expired: %v", err)
		}
	}
	if err := s.DestroyAllFor(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active session survived recovery revocation: %v", err)
	}
}
