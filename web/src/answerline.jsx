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
export function useAnswerLine(line) {
  const [cursor, setCursor] = useState(0)
  const [autoplay, setAutoplay] = useState(true)

  // A new line starts from the beginning and plays itself once.
  useEffect(() => {
    setCursor(0)
    setAutoplay(true)
  }, [line])

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
