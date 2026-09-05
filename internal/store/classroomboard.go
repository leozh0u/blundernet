package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// The demonstration board: what the coach is showing the room right now.
//
// This is the thing a chess lesson is actually built around. A coach sets a
// position up, talks over it, drags a piece to show the idea, takes it back,
// and the class watches the whole time. The first version of this classroom
// only ever put a position in front of students at the moment a question was
// asked, which makes it a quiz rather than a lesson.
//
// It lives in Redis rather than Postgres because it is a broadcast, not a
// record. Nobody wants yesterday's demo board back, and writing every drag of
// a bishop to a table would be a lot of rows for something whose whole value
// is that it is current.
type ClassBoard struct{ rdb *redis.Client }

func NewClassBoard(rdb *redis.Client) *ClassBoard { return &ClassBoard{rdb: rdb} }

// A room left open overnight should not still be showing last night's
// position, and a lesson does not run longer than this.
const boardTTL = 12 * time.Hour

// Board is what every student in the room sees.
type Board struct {
	// FEN carries the position. It is not required to be legal: a coach
	// demonstrating a pawn structure may have no kings on the board at all,
	// and refusing that would be refusing the most ordinary thing a coach
	// does.
	FEN string `json:"fen"`
	// Orientation is which way the board is turned, because a lesson about
	// Black's defence is taught from Black's side.
	Orientation string `json:"orientation"`
	// Caption is what the coach is saying about it, optional.
	Caption string `json:"caption,omitempty"`
	// Live is false when the coach has taken the board down, which leaves
	// students a clear "nothing right now" rather than a stale position.
	Live bool `json:"live"`
}

func boardKey(classroomID string) string     { return "classroom:board:" + classroomID }
func boardChannel(classroomID string) string { return "classroom:events:" + classroomID }

// Show publishes the board to everyone watching and stores it so that somebody
// joining late sees the same thing.
//
// Storing and publishing both matter. Publish alone loses the position for
// anyone who opens the page a second later, which in a classroom is most of
// the room; store alone means students only see changes when they refresh.
func (b *ClassBoard) Show(ctx context.Context, classroomID string, board Board) error {
	raw, err := json.Marshal(board)
	if err != nil {
		return err
	}
	pipe := b.rdb.TxPipeline()
	pipe.Set(ctx, boardKey(classroomID), raw, boardTTL)
	pipe.Publish(ctx, boardChannel(classroomID), raw)
	_, err = pipe.Exec(ctx)
	return err
}

// Current is the board as it stands, for a student arriving mid lesson.
func (b *ClassBoard) Current(ctx context.Context, classroomID string) (Board, error) {
	raw, err := b.rdb.Get(ctx, boardKey(classroomID)).Result()
	if errors.Is(err, redis.Nil) {
		return Board{Live: false}, nil
	}
	if err != nil {
		return Board{}, err
	}
	var out Board
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Board{}, err
	}
	return out, nil
}

// Watch is the stream of board changes for one room. Cancel the context to
// stop watching.
func (b *ClassBoard) Watch(ctx context.Context, classroomID string) <-chan []byte {
	sub := b.rdb.Subscribe(ctx, boardChannel(classroomID))
	out := make(chan []byte, 16)
	go func() {
		defer close(out)
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-sub.Channel():
				if !ok {
					return
				}
				out <- []byte(msg.Payload)
			}
		}
	}()
	return out
}

// Role reports what the caller is in this room, and refuses anyone who is not
// in it. Exported because the live board is served over a WebSocket, which
// sits in the api package and still has to answer "may you watch this".
func (c *Classrooms) Role(ctx context.Context, classroomID, userID string) (string, error) {
	return c.roleOf(ctx, classroomID, userID)
}
