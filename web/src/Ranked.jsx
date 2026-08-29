import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { sound } from './sound.js'
import { buildLine, useAnswerLine, MoveNavigator } from './answerline.jsx'
import { useBoardMotion, useBoardWidth } from './board.js'
import BoardOverlay from './BoardOverlay.jsx'

// Ranked mode. One puzzle at your level, no filters, no hints, no second try,
// and the rating moves both ways.
//
// The browser never sees the solution: each move is posted and graded by the
// server, which is the difference between a rating that means something and a
// score the client could award itself. That also means this component knows
// whether a move was right only after the round trip.

const uciOf = (mv) => mv.from + mv.to + (mv.promotion || '')

const askToSignUp = () =>
  window.dispatchEvent(new CustomEvent('blundernet:auth', { detail: 'signup' }))

export default function Ranked() {
  const [puzzle, setPuzzle] = useState(null)
  const [board, setBoard] = useState(null)
  const [phase, setPhase] = useState('loading') // loading, locked, setup, solving, done, empty
  const [result, setResult] = useState(null)
  const [answer, setAnswer] = useState(null) // {moves, fens} once an attempt is missed
  const nav = useAnswerLine(answer)
  const [selected, setSelected] = useState(null)
  const [verdict, setVerdict] = useState(null)
  const motion = useBoardMotion()
  const [frame, boardWidth] = useBoardWidth()
  const [me, setMe] = useState(null)
  const [error, setError] = useState('')
  const startedAt = useRef(0)
  const timers = useRef([])

  const later = useCallback((fn, ms) => {
    timers.current.push(setTimeout(fn, ms))
  }, [])
  const clearLater = useCallback(() => {
    timers.current.forEach(clearTimeout)
    timers.current = []
  }, [])
  useEffect(() => clearLater, [clearLater])

  const loadMe = useCallback(async () => {
    const res = await fetch('/api/puzzles/ranked/me')
    if (res.ok) setMe(await res.json())
  }, [])

  const present = useCallback(
    (p) => {
      clearLater()
      setPuzzle(p)
      setResult(null)
      setSelected(null)
      setVerdict(null)
      motion.jump()
      setPhase('setup')
      setBoard(new Chess(p.fen))
      later(() => {
        const after = new Chess(p.fen)
        after.move({
          from: p.setup_move.slice(0, 2),
          to: p.setup_move.slice(2, 4),
          promotion: p.setup_move[4] || undefined,
        })
        setBoard(after)
        setPhase('solving')
        startedAt.current = Date.now()
      }, 600)
    },
    [clearLater, later],
  )

  const next = useCallback(async () => {
    setPhase('loading')
    setAnswer(null)
    setError('')
    const res = await fetch('/api/puzzles/ranked')
    if (res.status === 401) {
      setPhase('locked')
      return
    }
    if (!res.ok) {
      setError('No puzzle came back. Try again in a moment.')
      setPhase('empty')
      return
    }
    present(await res.json())
  }, [present])

  useEffect(() => {
    loadMe()
    next()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Signing up from the gate should drop straight into a puzzle rather than
  // leaving the page still telling somebody they need an account.
  useEffect(() => {
    const retry = () => {
      loadMe()
      next()
    }
    window.addEventListener('blundernet:authed', retry)
    return () => window.removeEventListener('blundernet:authed', retry)
  }, [loadMe, next])

  const send = async (uci, positionBefore, afterMine) => {
    const res = await fetch('/api/puzzles/ranked/move', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uci, ms: Date.now() - startedAt.current }),
    })
    if (!res.ok) {
      setError('That move could not be sent. Reload to pick the puzzle back up.')
      return
    }
    const body = await res.json()
    // Cleared on a timer rather than by whatever happens next. It used to come
    // down only when the following puzzle loaded, so the mark sat on the board
    // through the whole answer walk, pinned to a square the position had
    // already moved past.
    setVerdict({ square: uci.slice(2, 4), good: body.correct })
    later(() => setVerdict(null), 900)
    if (body.done) sound[body.correct ? 'solve' : 'fail']()
    else if (body.correct) sound.move()

    if (body.correct && !body.done) {
      // The reply is forced, so it is played rather than announced.
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

    setPhase('done')
    setResult(body)
    loadMe()
    if (!body.correct && body.solution) {
      // Rewind to before the mistake and hand back the real line to walk.
      const from = stepOf(positionBefore, puzzle)
      const line = buildLine(positionBefore, body.solution, from)
      motion.jump()
      setBoard(positionBefore)
      setAnswer(line)
    }
  }

  // The board follows the cursor whenever there is an answer to walk, so every
  // way of moving through it lands in one place.
  useEffect(() => {
    if (!nav.fen) return
    motion.step(nav.cursor)
    setBoard(new Chess(nav.fen))
  }, [nav.fen, nav.cursor, motion.step])

  // How many plies of the solution had been played when the attempt ended.
  // The board history holds the setup move plus everything since.
  const stepOf = (position, p) => {
    if (!p) return 0
    return Math.max(0, position.history().length - 1)
  }

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

  if (phase === 'locked') {
    return (
      <section className="ranked-gate">
        <h2>Ranked needs an account</h2>
        <p>
          Everything else here works signed out. This one does not, because it
          keeps a rating: one puzzle at your level, no filters, no hints, and
          the number moves both ways. There is nowhere to keep it without an
          account.
        </p>
        <p>
          Want to practise instead? Learning mode is unlimited, filterable and
          free, and nothing there is scored.
        </p>
        <button onClick={askToSignUp}>Create an account</button>
      </section>
    )
  }

  const change = result?.result?.change

  return (
    <div className="puzzle-body">
      <div className="board-wrap">
        <div className="frame" ref={frame}>
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
          <BoardOverlay orientation={puzzle?.color || 'white'} badge={verdict} />
        </div>
      </div>

      <aside className="panel">
        <div className={`state ${result && !result.correct ? 'wrong' : 'yours'}`}>
          {phase === 'loading' && <span className="head">Finding your puzzle</span>}
          {phase === 'setup' && <span className="head">Watch the blunder</span>}
          {phase === 'solving' && puzzle && (
            <>
              <span className="head">
                {puzzle.color === 'white' ? 'White' : 'Black'} to play
              </span>
              {/* No solution length here on purpose. Knowing it is two moves
                  tells you when to stop calculating, and ranked is meant to
                  measure you rather than help you. Learning mode still shows
                  it. The server does not send it either, so this is not a
                  matter of the client choosing to be discreet. */}
              <span className="hint">Find the best move</span>
            </>
          )}
          {phase === 'done' && (
            <>
              <span className="head">{result.correct ? 'Solved' : 'Missed'}</span>
              <span className="hint">
                {change > 0 ? '+' : ''}
                {Math.round(change)} rating
              </span>
            </>
          )}
          {phase === 'empty' && <span className="head">{error}</span>}
        </div>

        <div className="record">
          <h3>Ranked</h3>
          <dl className="meta">
            <div>
              <dt>Your rating</dt>
              <dd>{me ? Math.round(me.rating) : '...'}</dd>
            </div>
            <div>
              <dt>Solved</dt>
              <dd>{me ? me.solved : '...'}</dd>
            </div>
            {phase === 'done' && (
              <div>
                <dt>Puzzle rating</dt>
                <dd>{result.puzzle_rating}</dd>
              </div>
            )}
          </dl>
          {phase === 'done' && <MoveNavigator line={answer} nav={nav} />}
          {phase === 'done' && result.explanation && (
            <div className="explain">
              <p className="headline">{result.explanation.headline}</p>
              {(result.explanation.points || []).map((pt) => (
                <p key={pt} className="point">
                  {pt}
                </p>
              ))}
            </div>
          )}
        </div>

        {phase === 'done' && (
          <div className="after">
            <button className="wide" onClick={next}>
              Next puzzle
            </button>
          </div>
        )}
        {error && phase !== 'empty' && <p className="form-error">{error}</p>}
      </aside>
    </div>
  )
}
