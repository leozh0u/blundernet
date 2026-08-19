// Package game holds the chess domain model. A Game is a replayable move
// list plus metadata; the chess rules themselves come from notnil/chess.
package game

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/notnil/chess"
)

// Modes a game can be played in. Rated games move the ladder and the rating;
// learning games move neither and allow hints.
const (
	ModeRated    = "rated"
	ModeLearning = "learning"
	// A game between two people. The bot never gets a job for one, and
	// nothing about it is rated: there is no second Glicko player here and
	// inventing one from a link somebody shared would be a rating anybody
	// could farm with a second tab.
	ModeFriend = "friend"
)

type Status string

const (
	StatusOngoing  Status = "ongoing"
	StatusFinished Status = "finished"
)

var (
	ErrNotYourTurn = errors.New("not your turn")
	ErrIllegalMove = errors.New("illegal move")
	ErrFinished    = errors.New("game is finished")
)

// Game is the unit of state stored in Redis. Moves are UCI strings; the
// full position is always reconstructed by replaying them, so the stored
// state can never drift from the rules engine.
type Game struct {
	ID          string   `json:"id"`
	Moves       []string `json:"moves"`
	Ply         int      `json:"ply"`
	PlayerColor string   `json:"player_color"` // "white" or "black"
	// Empty for anonymous games, which stay supported. The id travels with
	// the game through Redis because the archive write happens in the worker,
	// which has no session and no way to ask who started this.
	UserID string `json:"user_id,omitempty"`
	// The bot's strength for this game, and whether the result moves ratings.
	// Both travel with the game because the worker picks the move and the
	// archive write happens there too, with no session to ask.
	Level int  `json:"level"`
	Rated bool `json:"rated"`
	// Set on a game between two people. OpponentID fills in when the second
	// person opens the link, and is what decides which side a request is
	// allowed to move.
	Friend      bool      `json:"friend,omitempty"`
	OpponentID  string    `json:"opponent_id,omitempty"`
	Status      Status    `json:"status"`
	Result      string    `json:"result"`      // "1-0", "0-1", "1/2-1/2", ""
	Termination string    `json:"termination"` // "checkmate", "stalemate", "resignation", ...
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func New(id, playerColor string, level int, rated bool) *Game {
	now := time.Now().UTC()
	return &Game{
		ID:          id,
		Moves:       []string{},
		PlayerColor: playerColor,
		Level:       level,
		Rated:       rated,
		Status:      StatusOngoing,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ColorFor returns the side this user is allowed to move, and false when they
// are watching rather than playing. In a game against the bot the creator has
// one side and the bot has the other; in a friend game the second seat
// belongs to whoever opened the link first.
func (g *Game) ColorFor(userID string) (string, bool) {
	if g.Friend {
		switch userID {
		case "":
			return "", false
		case g.UserID:
			return g.PlayerColor, true
		case g.OpponentID:
			return g.EngineColor(), true
		}
		return "", false
	}
	// A game against the bot has one seat and it belongs to whoever created
	// it. This used to return true for anybody, on the reasoning that a bot
	// game has no second player to protect against. That was wrong: the game
	// id is not a credential, it appears in the address bar and in any
	// screenshot, and a rated game against the bot moves a real rating. Anyone
	// holding an id could move in or resign somebody else's game, and a forced
	// resignation cost the player over three hundred points.
	//
	// Every game gets an owner at creation, because creating one mints a guest
	// account when there is no session. The empty check is for rows made
	// before that was true, where the id is the only credential the game has.
	if g.UserID != "" && userID != g.UserID {
		return "", false
	}
	return g.PlayerColor, true
}

// EngineColor returns the side the engine plays. In a friend game it is the
// side the second player takes.
func (g *Game) EngineColor() string {
	if g.PlayerColor == "white" {
		return "black"
	}
	return "white"
}

// replay rebuilds the notnil/chess game from the move list. Moves in the
// list were validated on the way in, so any replay error is a bug.
func (g *Game) replay() (*chess.Game, error) {
	cg := chess.NewGame(chess.UseNotation(chess.UCINotation{}))
	for _, m := range g.Moves {
		if err := cg.MoveStr(m); err != nil {
			return nil, fmt.Errorf("replay %q: %w", m, err)
		}
	}
	return cg, nil
}

func (g *Game) FEN() string {
	cg, err := g.replay()
	if err != nil {
		return ""
	}
	return cg.Position().String()
}

// Turn returns "white" or "black" for the side to move.
func (g *Game) Turn() string {
	if len(g.Moves)%2 == 0 {
		return "white"
	}
	return "black"
}

// ApplyMove validates and applies one UCI move for the given color,
// updating status/result if the move ends the game.
func (g *Game) ApplyMove(color, uci string) error {
	if g.Status == StatusFinished {
		return ErrFinished
	}
	if g.Turn() != color {
		return ErrNotYourTurn
	}
	cg, err := g.replay()
	if err != nil {
		return err
	}
	if err := cg.MoveStr(strings.ToLower(strings.TrimSpace(uci))); err != nil {
		return fmt.Errorf("%w: %s", ErrIllegalMove, uci)
	}
	g.Moves = append(g.Moves, strings.ToLower(strings.TrimSpace(uci)))
	g.Ply = len(g.Moves)
	g.UpdatedAt = time.Now().UTC()
	if outcome := cg.Outcome(); outcome != chess.NoOutcome {
		g.Status = StatusFinished
		g.Result = outcome.String()
		g.Termination = methodString(cg.Method())
	}
	return nil
}

// Resign ends the game in favor of the opposite side.
func (g *Game) Resign(color string) error {
	if g.Status == StatusFinished {
		return ErrFinished
	}
	g.Status = StatusFinished
	g.Termination = "resignation"
	if color == "white" {
		g.Result = "0-1"
	} else {
		g.Result = "1-0"
	}
	g.UpdatedAt = time.Now().UTC()
	return nil
}

func methodString(m chess.Method) string {
	switch m {
	case chess.Checkmate:
		return "checkmate"
	case chess.Stalemate:
		return "stalemate"
	case chess.ThreefoldRepetition:
		return "threefold repetition"
	case chess.FivefoldRepetition:
		return "fivefold repetition"
	case chess.FiftyMoveRule:
		return "fifty-move rule"
	case chess.SeventyFiveMoveRule:
		return "seventy-five-move rule"
	case chess.InsufficientMaterial:
		return "insufficient material"
	default:
		return "unknown"
	}
}
