package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Show has to do two things and it is easy to get away with only one: publish
// so the room sees the change, and store so somebody arriving a moment later
// sees the same board. A student joining mid lesson only ever reads the stored
// copy, so losing it means the board is blank for them until the coach happens
// to touch a piece.
func TestShowStoresAndPublishes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b := NewClassBoard(rdb)
	ctx := context.Background()

	const room = "room-1"
	want := Board{FEN: "8/8/8/4k3/8/4K3/8/8 w - - 0 1", Orientation: "black", Live: true}

	watching := b.Watch(ctx, room)

	// Publish on a loop until the watcher hears one, rather than publishing
	// once and waiting.
	//
	// Watch subscribes on a goroutine, so there is a window where the channel
	// exists and Redis has not registered the subscriber yet. A single publish
	// into that window is dropped, and the first version of this test then sat
	// on a bare receive until the whole package hit the ten minute timeout. It
	// passed on a fast laptop and deadlocked on CI, which is the worst way for
	// a test to be wrong.
	deadline := time.After(10 * time.Second)
	var heard []byte
	for heard == nil {
		if err := b.Show(ctx, room, want); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-watching:
			heard = raw
		case <-time.After(25 * time.Millisecond):
		case <-deadline:
			t.Fatal("nothing reached a watcher, so nobody in the room would see the board")
		}
	}
	if len(heard) == 0 {
		t.Error("published an empty board")
	}

	got, err := b.Current(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	if got.FEN != want.FEN || got.Orientation != want.Orientation || !got.Live {
		t.Errorf("stored %+v, want %+v", got, want)
	}
}

// An empty room answers "nothing on the board" rather than failing, because a
// classroom with no lesson running is the normal state.
func TestCurrentIsEmptyBeforeAnyLesson(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b := NewClassBoard(rdb)

	got, err := b.Current(context.Background(), "never-used")
	if err != nil {
		t.Fatal(err)
	}
	if got.Live || got.FEN != "" {
		t.Errorf("an untouched room reported %+v", got)
	}
}
