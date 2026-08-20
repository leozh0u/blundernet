import { useCallback, useEffect, useState } from 'react'
import { Chess } from 'chess.js'

// Walking the answer to a puzzle you missed.
//
// Both drill modes need this and they needed it for the same reason: an
// animation that plays once and stops tells you what the moves were but not
// why, and the position you actually want to look at is usually two plies back
// from where it ends. So the line is computed up front, every position along
// it is kept, and a cursor decides which one the board shows. Buttons, arrow
// keys and clicking a move are then three ways of moving the same cursor
// rather than three features that have to agree with each other.

// puzzleLine builds the walkable history of a puzzle: its starting position,
// the opponent move that sets it up, and however much of the solution has been
// played so far. Passing the whole solution length gives the finished puzzle.
//
// Derived rather than accumulated. The board is replaced on every move, on
// every hint and on a reveal, so a separate list of positions kept alongside
// it would be a second source of truth with several chances to disagree. The
// puzzle and how far into it you are is all the information there is.
export function puzzleLine(puzzle, played) {
  if (!puzzle) return null
  const board = new Chess(puzzle.fen)
  const setup = puzzle.setup_move
  const fens = [board.fen()]
  const moves = []
  const push = (uci) => {
    let mv
    try {
      mv = board.move({
        from: uci.slice(0, 2),
        to: uci.slice(2, 4),
        promotion: uci[4] || undefined,
      })
    } catch {
      return false
    }
    const white = mv.color === 'w'
    const number = white ? board.moveNumber() : board.moveNumber() - 1
    moves.push({ san: mv.san, white, number, lead: moves.length === 0 && !white })
    fens.push(board.fen())
    return true
  }
  // The setup move is part of the story: it is the mistake the puzzle is
  // about, and walking back to see it is most of the point of walking back.
  if (setup && !push(setup)) return { moves, fens }
  for (let i = 0; i < Math.min(played, puzzle.solution.length); i++) {
    if (!push(puzzle.solution[i])) break
  }
  return { moves, fens }
}

// buildLine replays the solution from a position and keeps every step.
// fens[0] is the position before the first correct move, so cursor n means n
// plies of the answer have been played.
export function buildLine(position, solution, from) {
  const replay = new Chess(position.fen())
  const moves = []
  const fens = [replay.fen()]
  for (let i = from; i < solution.length; i++) {
    const uci = solution[i]
    let mv
    try {
      mv = replay.move({
        from: uci.slice(0, 2),
        to: uci.slice(2, 4),
        promotion: uci[4] || undefined,
      })
    } catch {
      // A line that will not replay is a corpus problem, not a user problem.
      // Showing the moves so far beats blanking the panel.
      break
    }
    const white = mv.color === 'w'
    // moveNumber() after a black move has already rolled to the next one, so a
    // line starting on black takes the number it was actually played on.
    const number = white ? replay.moveNumber() : replay.moveNumber() - 1
    moves.push({ san: mv.san, white, number, lead: moves.length === 0 && !white })
    fens.push(replay.fen())
  }
  return { moves, fens }
}

// useAnswerLine owns the cursor for a line, including the one-time playthrough
// and the keyboard. Pass null when there is no answer on screen.
// useAnswerLine owns the cursor for a line.
//
// startAtEnd is what separates the two uses. A puzzle you are still solving
// should sit at the live position with history behind it, so the cursor starts
// at the end and nothing plays itself. An answer you just missed should start
// at the beginning and play through once.
export function useAnswerLine(line, { startAtEnd = false } = {}) {
  const end = line ? line.fens.length - 1 : 0
  const [cursor, setCursor] = useState(startAtEnd ? end : 0)
  const [autoplay, setAutoplay] = useState(!startAtEnd)

  // The line grows as moves are played. Sitting at the end means "follow the
  // game"; sitting behind it means the reader has stepped back on purpose and
  // should not be yanked forward by the next move.
  useEffect(() => {
    if (!line) return
    setCursor((c) => (startAtEnd ? (c >= line.fens.length - 2 ? line.fens.length - 1 : c) : 0))
    setAutoplay(!startAtEnd)
  }, [line, startAtEnd])

  const atStart = cursor === 0
  const atEnd = !line || cursor >= line.fens.length - 1

  const step = useCallback(
    (delta) => {
      setAutoplay(false) // any deliberate move ends the playthrough
      setCursor((c) => (line ? Math.min(Math.max(c + delta, 0), line.fens.length - 1) : c))
    },
    [line],
  )

  const jumpTo = useCallback((i) => {
    setAutoplay(false)
    setCursor(i)
  }, [])

  // The line plays itself once, because the first thing anybody wants after
  // missing is to see the answer without doing any work. It stops the moment
  // they touch anything, which is what makes it a courtesy rather than
  // something to sit through.
  useEffect(() => {
    if (!line || !autoplay || atEnd) return
    const t = setTimeout(() => setCursor((c) => c + 1), cursor === 0 ? 600 : 700)
    return () => clearTimeout(t)
  }, [line, autoplay, atEnd, cursor])

  // Left and right walk the line, the way they do on every board online.
  // Ignored while typing, so the filter fields keep working.
  useEffect(() => {
    if (!line) return
    const onKey = (e) => {
      const tag = (e.target.tagName || '').toLowerCase()
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || e.target.isContentEditable) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const go = {
        ArrowLeft: () => step(-1),
        ArrowRight: () => step(1),
        Home: () => jumpTo(0),
        End: () => jumpTo(line.fens.length - 1),
      }[e.key]
      if (!go) return
      e.preventDefault()
      go()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [line, step, jumpTo])

  return { cursor, atStart, atEnd, step, jumpTo, fen: line ? line.fens[cursor] : null }
}

// MoveNavigator is the written line plus its controls. Each move is a button
// that jumps the board to it, so reading the score and navigating it are the
// same gesture rather than two controls that can disagree.
export function MoveNavigator({ line, nav, heading = 'The answer' }) {
  if (!line || line.moves.length === 0) return null
  return (
    <div className="answer">
      <h4>{heading}</h4>
      <p className="answer-line">
        {line.moves.map((m, i) => (
          <button
            type="button"
            key={i}
            className={`answer-move ${nav.cursor === i + 1 ? 'on' : ''}`}
            onClick={() => nav.jumpTo(i + 1)}
            aria-current={nav.cursor === i + 1 ? 'step' : undefined}
          >
            {(m.white || m.lead) && (
              <span className="answer-no">
                {m.number}
                {m.white ? '.' : '...'}
              </span>
            )}
            <span className="answer-san">{m.san}</span>
          </button>
        ))}
      </p>
      <div className="answer-nav">
        <button type="button" onClick={() => nav.jumpTo(0)} disabled={nav.atStart} aria-label="Back to the start" title="Start">
          &#124;&lt;
        </button>
        <button type="button" onClick={() => nav.step(-1)} disabled={nav.atStart} aria-label="Previous move" title="Previous (left arrow)">
          &lt;
        </button>
        <button type="button" onClick={() => nav.step(1)} disabled={nav.atEnd} aria-label="Next move" title="Next (right arrow)">
          &gt;
        </button>
        <button type="button" onClick={() => nav.jumpTo(line.fens.length - 1)} disabled={nav.atEnd} aria-label="To the end" title="End">
          &gt;&#124;
        </button>
        <span className="answer-hint">Arrow keys work too</span>
      </div>
    </div>
  )
}
