import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { sound } from './sound.js'
import { useBoardMotion, useBoardWidth, useBlindfold } from './board.js'
import { PositionText, BlindfoldButton } from './Position.jsx'
import { auth } from './auth.js'
import GuestNote from './GuestNote.jsx'
import BoardOverlay from './BoardOverlay.jsx'

// Streak. Puzzles get harder until you miss one, and then the run is over.
// No hints, no second try, no rating: the only thing kept is the best run.
//
// Same server-held shape as ranked, because a number people compare has to be
// graded somewhere the browser cannot reach.

const uciOf = (mv) => mv.from + mv.to + (mv.promotion || '')

export default function Streak() {
  const [puzzle, setPuzzle] = useState(null)
  const [board, setBoard] = useState(null)
  const [phase, setPhase] = useState('idle') // idle, setup, solving, over
  const [count, setCount] = useState(0)
  const [best, setBest] = useState(0)
  const [guest, setGuest] = useState(false)
  const [selected, setSelected] = useState(null)
  const [verdict, setVerdict] = useState(null)
  const motion = useBoardMotion()
  const blindfold = useBlindfold()
  const [frame, boardWidth] = useBoardWidth()
  const [error, setError] = useState('')
  const startedAt = useRef(0)
  const timers = useRef([])

  const later = useCallback((fn, ms) => {
    timers.current.push(setTimeout(fn, ms))
  }, [])
  useEffect(
    () => () => {
      timers.current.forEach(clearTimeout)
    },
    [],
  )

  // Who is playing, once, so the panel can say whether the best run survives
  // closing the tab. A guest streak is real and rated and kept, just kept in a
  // cookie.
  useEffect(() => {
    auth.me().then((u) => setGuest(!u || u.guest)).catch(() => {})
  }, [])

  const present = useCallback(
    (p) => {
      setPuzzle(p)
      setSelected(null)
      setCount(p.count)
      setBest(p.best)
      setPhase('setup')
      setVerdict(null)
      // A different puzzle. Nothing on the old board relates to the new one,
      // so it appears rather than sliding into place.
      motion.jump(0)
      setBoard(new Chess(p.fen))
      later(() => {
        const after = new Chess(p.fen)
        after.move({
          from: p.setup_move.slice(0, 2),
          to: p.setup_move.slice(2, 4),
          promotion: p.setup_move[4] || undefined,
        })
        // The blunder that sets the puzzle up is a move, and looks like one.
        motion.move()
        setBoard(after)
        setPhase('solving')
        startedAt.current = Date.now()
      }, 600)
    },
    [later, motion.jump, motion.move],
  )

  const start = useCallback(async () => {
    setError('')
    const res = await fetch('/api/puzzles/streak', { method: 'POST' })
    if (!res.ok) {
      setError('No puzzle came back. Try again in a moment.')
      return
    }
    present(await res.json())
  }, [present])

  const send = async (uci, positionBefore, afterMine) => {
    const res = await fetch('/api/puzzles/streak/move', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uci, ms: Date.now() - startedAt.current }),
    })
    if (!res.ok) {
      setError('That move could not be sent. Reload to pick the run back up.')
      return
    }
    const body = await res.json()
    // This used to be set and never cleared, anywhere in the file, so the
    // mark stayed on the board for the rest of the run and ended up pinned to
    // a square belonging to a puzzle two positions ago.
    setVerdict({ square: uci.slice(2, 4), good: body.correct })
    later(() => setVerdict(null), 900)
    setCount(body.count)

    if (body.correct && !body.done) {
      later(() => {
        const answered = new Chess(afterMine.fen())
        answered.move({
          from: body.reply.slice(0, 2),
          to: body.reply.slice(2, 4),
          promotion: body.reply[4] || undefined,
        })
        motion.move()
        setBoard(answered)
      }, 350)
      return
    }
    if (body.correct) {
      // Solved. Straight into the next one, one rung harder.
      later(start, 700)
      return
    }

    setPhase('over')
    if (body.best) setBest(body.best)
    if (body.solution) {
      let running = positionBefore
      let delay = 500
      for (const uciMove of body.solution.slice(stepOf(positionBefore))) {
        const snapshot = new Chess(running.fen())
        try {
          snapshot.move({
            from: uciMove.slice(0, 2),
            to: uciMove.slice(2, 4),
            promotion: uciMove[4] || undefined,
          })
        } catch {
          break
        }
        running = snapshot
        const shown = new Chess(snapshot.fen())
        later(() => {
          motion.move()
          setBoard(shown)
        }, delay)
        delay += 550
      }
    }
  }

  const stepOf = (position) => Math.max(0, position.history().length - 1)

  const tryMove = (from, to) => {
    if (phase !== 'solving' || !board) return false
    const probe = new Chess(board.fen())
    let mv
    try {
      mv = probe.move({ from, to, promotion: 'q' })
    } catch {
      return false
    }
    if (!mv) return false
    setSelected(null)
    const before = new Chess(board.fen())
    setBoard(probe)
    send(uciOf(mv), before, probe)
    return true
  }

  const onSquareClick = (square) => {
    if (phase !== 'solving' || !board) return
    if (selected === square) return setSelected(null)
    if (selected && tryMove(selected, square)) return
    const piece = board.get(square)
    const mine = piece && piece.color === (puzzle.color === 'white' ? 'w' : 'b')
    setSelected(mine ? square : null)
  }

  const legalTargets = useMemo(() => {
    if (!selected || !board) return []
    return board.moves({ square: selected, verbose: true }).map((m) => m.to)
  }, [selected, board])

  const squareStyles = useMemo(() => {
    const styles = {}
    const history = board?.history({ verbose: true }) || []
    const last = history[history.length - 1]
    if (last) {
      for (const sq of [last.from, last.to]) {
        styles[sq] = { background: 'rgba(246, 200, 92, 0.45)' }
      }
    }
    if (selected) styles[selected] = { background: 'rgba(246, 200, 92, 0.6)' }
    for (const sq of legalTargets) {
      styles[sq] = {
        ...styles[sq],
        background: 'radial-gradient(circle, rgba(16,28,40,0.42) 20%, transparent 22%)',
      }
    }
    return styles
  }, [selected, legalTargets, board])

  if (phase === 'idle') {
    return (
      <section className="ranked-gate">
        <h2>Streak</h2>
        <p>
          Puzzles get harder every time you solve one. Miss a single move and
          the run ends. Nothing here moves a rating.
        </p>
        <button onClick={start}>Start a run</button>
        {error && <p className="form-error">{error}</p>}
      </section>
    )
  }

  return (
    <div className="puzzle-body">
      <div className="board-wrap">
        <div className={`frame ${blindfold.className}`} ref={frame}>
          {boardWidth > 0 && (
          <Chessboard
            boardWidth={boardWidth}
            position={board ? board.fen() : 'start'}
            animationDuration={motion.ms}
            onPieceDrop={(f, t) => tryMove(f, t)}
            onSquareClick={onSquareClick}
            boardOrientation={puzzle?.color || 'white'}
            arePiecesDraggable={phase === 'solving'}
            customBoardStyle={{ borderRadius: '6px' }}
            customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
            customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
            customSquareStyles={squareStyles}
          />
          )}
          <PositionText board={board} />
          <BoardOverlay orientation={puzzle?.color || 'white'} badge={verdict} />
        </div>
      </div>

      <aside className="panel">
        <div className={`state ${phase === 'over' ? 'wrong' : 'yours'}`}>
          {phase === 'setup' && <span className="head">Watch the blunder</span>}
          {phase === 'solving' && puzzle && (
            <span className="head">
              {puzzle.color === 'white' ? 'White' : 'Black'} to play
            </span>
          )}
          {phase === 'over' && <span className="head">Run over</span>}
        </div>

        <div className="record">
          <h3>Streak</h3>
          <dl className="meta">
            <div>
              <dt>This run</dt>
              <dd>{count}</dd>
            </div>
            <div>
              <dt>Best</dt>
              <dd>{best}</dd>
            </div>
          </dl>
          {guest && <GuestNote text="Your best run is saved to this browser only." />}
        </div>

        {phase === 'over' && (
          <div className="after">
            <button className="wide" onClick={start}>
              Start again
            </button>
          </div>
        )}
        {/* Available while solving, since playing a streak blindfold is the
            whole point of having it here. */}
        {phase === 'solving' && (
          <div className="during">
            <BlindfoldButton on={blindfold.on} onToggle={blindfold.toggle} />
          </div>
        )}
        {error && <p className="form-error">{error}</p>}
      </aside>
    </div>
  )
}
