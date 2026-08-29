package review

import (
	"errors"
	"regexp"
	"strings"

	"github.com/notnil/chess"
)

// Reading a game somebody pasted in.
//
// The point of accepting PGN is that it makes this a tool rather than a
// feature of this site: the game a coach wants to go through with a student is
// almost never one played here. Every chess site exports PGN, so "paste your
// game" is the whole import story.
//
// Parsing is deliberately forgiving. What arrives from a copy and paste is
// rarely a clean PGN file: it has the header block or it does not, it has
// comments and clock annotations from Lichess, it has "1." or "1..." or
// neither, and it often has a stray blank line in the middle. All of that is
// noise around the only thing needed here, which is the moves in order.

var (
	ErrNoMoves  = errors.New("no moves found in that game")
	ErrTooLong  = errors.New("that game is too long to review")
	ErrNotChess = errors.New("that does not look like a chess game")
)

// maxPlies is about a hundred and twenty moves each. Long enough for any real
// game, short enough that one paste cannot occupy the engine for a minute.
const maxPlies = 240

var (
	// A header line: [White "someone"]
	headerLine = regexp.MustCompile(`(?m)^\[[^\]]*\]\s*$`)
	// Everything in braces, which is where comments and clock times live.
	braces = regexp.MustCompile(`\{[^}]*\}`)
	// Move numbers: "12." or "12..."
	moveNumber = regexp.MustCompile(`\b\d+\.(\.\.)?`)
	// Numeric annotation glyphs: $1, $4
	nags = regexp.MustCompile(`\$\d+`)
	// Recursive variations. Nested ones are stripped by repeating.
	variation = regexp.MustCompile(`\([^()]*\)`)
)

// ParsePGN pulls the mainline moves out of a pasted game and returns them in
// UCI, which is the form everything downstream uses.
func ParsePGN(text string) ([]string, error) {
	s := headerLine.ReplaceAllString(text, " ")
	s = braces.ReplaceAllString(s, " ")
	// Variations are removed innermost first, so nesting unwinds.
	for i := 0; i < 8; i++ {
		stripped := variation.ReplaceAllString(s, " ")
		if stripped == s {
			break
		}
		s = stripped
	}
	s = moveNumber.ReplaceAllString(s, " ")
	s = nags.ReplaceAllString(s, " ")

	game := chess.NewGame()
	var moves []string
	for _, token := range strings.Fields(s) {
		switch token {
		case "1-0", "0-1", "1/2-1/2", "*":
			// The result, which ends the movetext.
			return done(moves)
		}
		// Annotations attached to a move: Qh5?! or Nf6??
		token = strings.TrimRight(token, "?!")
		if token == "" {
			continue
		}
		if err := game.MoveStr(token); err != nil {
			// One unreadable token is noise; the game so far still stands.
			// Refusing the whole paste over a stray character would make this
			// unusable on real copied text.
			continue
		}
		history := game.Moves()
		last := history[len(history)-1]
		pos := game.Positions()[len(history)-1]
		moves = append(moves, chess.UCINotation{}.Encode(pos, last))
		if len(moves) >= maxPlies {
			return nil, ErrTooLong
		}
	}
	return done(moves)
}

func done(moves []string) ([]string, error) {
	if len(moves) == 0 {
		return nil, ErrNoMoves
	}
	return moves, nil
}
