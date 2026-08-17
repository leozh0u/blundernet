import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'

// Learning mode. A drill, not a test: filter for exactly what you want to
// practise, and nothing here moves a rating. The filter lives in the URL, so
// a set of filters is a link and "another like this" is the same search
// seeded from the puzzle you are standing on.

const LENGTHS = [
  { label: '1 move', min: 1, max: 1 },
  { label: '2 moves', min: 2, max: 2 },
  { label: '3 moves', min: 3, max: 3 },
  { label: '4 moves', min: 4, max: 4 },
  { label: '5 or more', min: 5, max: 0 },
]

const PHASES = ['opening', 'middlegame', 'endgame']

// The themes worth putting in front of someone, in the order a player would
// look for them. The full list from the API is 73 long and most of the tail is
// either a phase, a length, or a note about the source game.
const COMMON_THEMES = [
  'fork', 'pin', 'skewer', 'discoveredAttack', 'doubleCheck', 'deflection',
  'attraction', 'clearance', 'interference', 'sacrifice', 'hangingPiece',
  'trappedPiece', 'backRankMate', 'smotheredMate', 'promotion',
  'underPromotion', 'zugzwang', 'quietMove', 'defensiveMove', 'intermezzo',
  'xRayAttack', 'capturingDefender', 'mateIn1', 'mateIn2', 'mateIn3',
]

const RATING_BANDS = [
  { label: 'Any rating', min: 0, max: 0 },
  { label: 'Under 1200', min: 0, max: 1200 },
  { label: '1200 to 1500', min: 1200, max: 1500 },
  { label: '1500 to 1800', min: 1500, max: 1800 },
  { label: '1800 to 2100', min: 1800, max: 2100 },
  { label: '2100 to 2400', min: 2100, max: 2400 },
  { label: 'Over 2400', min: 2400, max: 0 },
]

// Lichess theme names are camelCase identifiers. Splitting on capitals gets
// most of them readable; the rest are spelled out, because "Mate In1" is worse
// than no label at all.
const LABELS = {
  mateIn1: 'Mate in 1',
  mateIn2: 'Mate in 2',
  mateIn3: 'Mate in 3',
  xRayAttack: 'X-ray attack',
  backRankMate: 'Back rank mate',
  discoveredAttack: 'Discovered attack',
  capturingDefender: 'Capturing the defender',
  hangingPiece: 'Hanging piece',
  trappedPiece: 'Trapped piece',
  smotheredMate: 'Smothered mate',
  quietMove: 'Quiet move',
  defensiveMove: 'Defensive move',
  underPromotion: 'Underpromotion',
}

const titleCase = (t) =>
  LABELS[t] || t.replace(/([A-Z])/g, ' $1').replace(/^./, (c) => c.toUpperCase())

// The filter is read from and written to the query string, so reload, back,
// and share all keep the drill you were doing.
function filterFromURL() {
  const q = new URLSearchParams(window.location.search)
  return {
    ratingMin: Number(q.get('rating_min') || 0),
    ratingMax: Number(q.get('rating_max') || 0),
    phases: (q.get('phase') || '').split(',').filter(Boolean),
    theme: q.get('theme') || '',
    movesMin: Number(q.get('moves_min') || 0),
    movesMax: Number(q.get('moves_max') || 0),
  }
}

function filterToQuery(f) {
  const q = new URLSearchParams()
  if (f.ratingMin) q.set('rating_min', f.ratingMin)
  if (f.ratingMax) q.set('rating_max', f.ratingMax)
  if (f.phases.length) q.set('phase', f.phases.join(','))
  if (f.theme) q.set('theme', f.theme)
  if (f.movesMin) q.set('moves_min', f.movesMin)
  if (f.movesMax) q.set('moves_max', f.movesMax)
  return q
}

const uciOf = (mv) => mv.from + mv.to + (mv.promotion || '')

const PIECE_NAMES = { p: 'pawn', n: 'knight', b: 'bishop', r: 'rook', q: 'queen', k: 'king' }

export default function Puzzles() {
  const [filter, setFilter] = useState(filterFromURL)
  const [queue, setQueue] = useState([])
  const [puzzle, setPuzzle] = useState(null)
  const [board, setBoard] = useState(null) // a Chess instance, replaced not mutated
  const [step, setStep] = useState(0) // how far into the solution we are
  const [phase, setPhase] = useState('loading') // loading, solving, solved, failed, empty
  const [selected, setSelected] = useState(null)
  const [hints, setHints] = useState(0)
  const [saved, setSaved] = useState(false)
  // 'search' is the corpus, the other two are your own lists. The account
  // page links straight into one of them, so the choice comes from the URL.
  const [source, setSource] = useState(
    () => new URLSearchParams(window.location.search).get('drill') || 'search',
  )
  const [error, setError] = useState('')
  const startedAt = useRef(0)
  const timers = useRef([])

  // Every delayed board update is cancellable, because moving on to the next
  // puzzle while the opponent's reply is still pending would otherwise play
  // that reply onto the new position.
  const later = useCallback((fn, ms) => {
    const id = setTimeout(fn, ms)
    timers.current.push(id)
  }, [])
  const clearLater = useCallback(() => {
    timers.current.forEach(clearTimeout)
    timers.current = []
  }, [])
  useEffect(() => clearLater, [clearLater])

  // The wrong-answer list is a different query, not a filter: it is driven by
  // your attempts rather than by the corpus, so it ignores the filter bar
  // instead of pretending to combine with it.
  const fetchBatch = useCallback(
    async (f) => {
      if (source !== 'search') {
        const path = source === 'wrong' ? 'failed' : 'favourites'
        const res = await fetch(`/api/puzzles/${path}?limit=10`)
        if (!res.ok) throw new Error('That list could not be loaded. Try again.')
        return (await res.json()).puzzles || []
      }
      const q = filterToQuery(f)
      q.set('limit', '10')
      const res = await fetch(`/api/puzzles?${q}`)
      if (!res.ok) throw new Error('The puzzle search failed. Try again.')
      const body = await res.json()
      return body.puzzles || []
    },
    [source],
  )

  // Show a puzzle: set the position before the blunder, then play the blunder
  // after a beat so you see what you are being asked to punish.
  const present = useCallback(
    (p) => {
      clearLater()
      setPuzzle(p)
      setStep(0)
      setSelected(null)
      setHints(0)
      setSaved(!!p.saved)
      setPhase('setup')
      const before = new Chess(p.fen)
      setBoard(before)
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

  const load = useCallback(
    async (f) => {
      setPhase('loading')
      setError('')
      try {
        const batch = await fetchBatch(f)
        if (batch.length === 0) {
          setPuzzle(null)
          setPhase('empty')
          return
        }
        setQueue(batch.slice(1))
        present(batch[0])
      } catch (e) {
        setError(e.message)
        setPhase('empty')
      }
    },
    [fetchBatch, present],
  )

  useEffect(() => {
    load(filter)
    // Only on mount and on an explicit filter change, which calls load itself.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Flipping between the search and the wrong-answer list reloads, since they
  // are two different queries rather than two views of one.
  const firstRender = useRef(true)
  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false
      return
    }
    load(filter)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source])

  const next = useCallback(async () => {
    if (queue.length > 0) {
      const [head, ...rest] = queue
      setQueue(rest)
      present(head)
      // Refill in the background so the last puzzle of a batch does not stall.
      if (rest.length <= 2) {
        fetchBatch(filter)
          .then((more) => setQueue((q) => [...q, ...more]))
          .catch(() => {})
      }
      return
    }
    await load(filter)
  }, [queue, filter, present, load, fetchBatch])

  // Saving is a toggle, and the star flips before the request lands. If the
  // write fails the star goes back, which is a better trade than making
  // somebody wait on a round trip to see a star fill in.
  const toggleSave = async () => {
    if (!puzzle) return
    const next = !saved
    setSaved(next)
    const res = await fetch(`/api/puzzles/${puzzle.id}/favourite`, {
      method: next ? 'POST' : 'DELETE',
    })
    if (!res.ok) setSaved(!next)
  }

  const record = useCallback((id, solved, hints) => {
    fetch(`/api/puzzles/${id}/attempt`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        solved,
        ms: Date.now() - startedAt.current,
        hints,
      }),
    }).catch(() => {})
  }, [])

  // Reveal the rest of the solution on the board, which is what a failed
  // puzzle owes you.
  const revealFrom = useCallback(
    (position, solution, from) => {
      let delay = 400
      let running = position
      for (let i = from; i < solution.length; i++) {
        const uci = solution[i]
        const snapshot = new Chess(running.fen())
        snapshot.move({
          from: uci.slice(0, 2),
          to: uci.slice(2, 4),
          promotion: uci[4] || undefined,
        })
        running = snapshot
        const shown = new Chess(snapshot.fen())
        later(() => setBoard(shown), delay)
        delay += 500
      }
    },
    [later],
  )

  // Giving up is a real thing people do, and without it the only way out of a
  // puzzle you cannot see is to guess wrong on purpose or skip it, which is
  // worse: the drill list would never learn you were stuck on this one.
  const showSolution = () => {
    if (phase !== 'solving' || !puzzle || !board) return
    setPhase('failed')
    record(puzzle.id, false, hints)
    revealFrom(new Chess(board.fen()), puzzle.solution, step)
  }

  const tryMove = (from, to) => {
    if (phase !== 'solving' || !puzzle || !board) return false
    const probe = new Chess(board.fen())
    let mv
    try {
      mv = probe.move({ from, to, promotion: 'q' })
    } catch {
      return false
    }
    if (!mv) return false
    setSelected(null)

    const want = puzzle.solution[step]
    // Exact match, or any move that is mate. A second mate in one is still a
    // solved puzzle, and refusing it would be the site being wrong.
    const right = uciOf(mv) === want || probe.isCheckmate()
    if (!right) {
      setBoard(probe)
      setPhase('failed')
      record(puzzle.id, false, hints)
      const truth = new Chess(board.fen())
      revealFrom(truth, puzzle.solution, step)
      return true
    }

    // Play the solver's move, then the opponent's forced reply.
    const played = new Chess(board.fen())
    played.move({
      from: want.slice(0, 2),
      to: want.slice(2, 4),
      promotion: want[4] || undefined,
    })
    setBoard(played)

    const nextStep = step + 1
    if (nextStep >= puzzle.solution.length) {
      setStep(nextStep)
      setPhase('solved')
      record(puzzle.id, true, hints)
      return true
    }
    const reply = puzzle.solution[nextStep]
    later(() => {
      const answered = new Chess(played.fen())
      answered.move({
        from: reply.slice(0, 2),
        to: reply.slice(2, 4),
        promotion: reply[4] || undefined,
      })
      setBoard(answered)
      setStep(nextStep + 1)
    }, 350)
    setStep(nextStep)
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

  // Hints come from the solution the browser already has, so they cost no
  // round trip. They go piece, then square, then the move itself, and the
  // count is sent with the attempt: solving cold and solving after three
  // hints are different things and the wrong-answer list should know.
  const hint = useMemo(() => {
    if (!puzzle || !board || hints === 0 || phase !== 'solving' || !puzzle.solution) {
      return null
    }
    const want = puzzle.solution[step]
    if (!want) return null
    const from = want.slice(0, 2)
    const to = want.slice(2, 4)
    const piece = board.get(from)
    const name = PIECE_NAMES[piece?.type] || 'piece'

    // First hint: the kind of piece, with every one of yours lit up. Second:
    // which one. Third: where it goes. Each one shows on the board, because a
    // hint that only prints a sentence in the corner reads as nothing
    // happening.
    if (hints === 1) {
      const mine = puzzle.color === 'white' ? 'w' : 'b'
      const squares = []
      for (const file of 'abcdefgh') {
        for (let rank = 1; rank <= 8; rank++) {
          const sq = file + rank
          const p = board.get(sq)
          if (p && p.type === piece?.type && p.color === mine) squares.push(sq)
        }
      }
      return { text: `A ${name} move.`, squares }
    }
    if (hints === 2) return { text: `The ${name} on ${from}.`, squares: [from] }

    const probe = new Chess(board.fen())
    const mv = probe.move({ from, to, promotion: want[4] || undefined })
    return { text: mv ? mv.san : want, squares: [from], target: to }
  }, [puzzle, board, hints, step, phase])

  const squareStyles = useMemo(() => {
    const styles = {}
    const history = board?.history({ verbose: true }) || []
    const last = history[history.length - 1]
    if (last) {
      for (const sq of [last.from, last.to]) {
        styles[sq] = { background: 'rgba(246, 200, 92, 0.45)' }
      }
    }
    for (const sq of hint?.squares || []) {
      styles[sq] = { background: 'rgba(74, 144, 217, 0.55)' }
    }
    if (hint?.target) {
      styles[hint.target] = { background: 'rgba(74, 144, 217, 0.3)' }
    }
    if (selected) styles[selected] = { background: 'rgba(246, 200, 92, 0.6)' }
    for (const sq of legalTargets) {
      styles[sq] = {
        ...styles[sq],
        background: 'radial-gradient(circle, rgba(16,28,40,0.42) 20%, transparent 22%)',
      }
    }
    return styles
  }, [selected, legalTargets, board, hint])

  const apply = (patch) => {
    const f = { ...filter, ...patch }
    setFilter(f)
    const q = filterToQuery(f)
    const url = q.toString() ? `/puzzles?${q}` : '/puzzles'
    window.history.replaceState({}, '', url)
    load(f)
  }

  const togglePhase = (p) => {
    const has = filter.phases.includes(p)
    apply({ phases: has ? filter.phases.filter((x) => x !== p) : [...filter.phases, p] })
  }

  // Another like this: the puzzle's own tags fed back into the search. No
  // similarity model, just the filter that already exists.
  const anotherLikeThis = () => {
    const theme = puzzle.themes.find((t) => COMMON_THEMES.includes(t)) || ''
    apply({
      theme,
      movesMin: puzzle.moves,
      movesMax: puzzle.moves,
      phases: [puzzle.phase],
      ratingMin: Math.max(0, Math.round((puzzle.rating - 150) / 100) * 100),
      ratingMax: Math.round((puzzle.rating + 150) / 100) * 100,
    })
  }

  const lengthActive = (l) => filter.movesMin === l.min && filter.movesMax === l.max
  const ratingActive = (b) => filter.ratingMin === b.min && filter.ratingMax === b.max

  return (
    <main className="puzzles">
      <section className="filters" aria-label="Puzzle filters">
        <div className="filter-row">
          <span className="filter-label">Drill</span>
          <div className="chips">
            <button
              className={`chip ${source === 'search' ? 'on' : ''}`}
              onClick={() => setSource('search')}
            >
              Search
            </button>
            <button
              className={`chip ${source === 'wrong' ? 'on' : ''}`}
              onClick={() => setSource('wrong')}
            >
              Ones I got wrong
            </button>
            <button
              className={`chip ${source === 'saved' ? 'on' : ''}`}
              onClick={() => setSource('saved')}
            >
              Saved
            </button>
          </div>
        </div>
        <div className="filter-row" data-hidden={source !== 'search' || undefined}>
          <span className="filter-label">Rating</span>
          <div className="chips">
            {RATING_BANDS.map((b) => (
              <button
                key={b.label}
                className={`chip ${ratingActive(b) ? 'on' : ''}`}
                onClick={() => apply({ ratingMin: b.min, ratingMax: b.max })}
              >
                {b.label}
              </button>
            ))}
          </div>
        </div>

        <div className="filter-row" data-hidden={source !== 'search' || undefined}>
          <span className="filter-label">Length</span>
          <div className="chips">
            <button
              className={`chip ${!filter.movesMin && !filter.movesMax ? 'on' : ''}`}
              onClick={() => apply({ movesMin: 0, movesMax: 0 })}
            >
              Any
            </button>
            {LENGTHS.map((l) => (
              <button
                key={l.label}
                className={`chip ${lengthActive(l) ? 'on' : ''}`}
                onClick={() => apply({ movesMin: l.min, movesMax: l.max })}
              >
                {l.label}
              </button>
            ))}
          </div>
        </div>

        <div className="filter-row" data-hidden={source !== 'search' || undefined}>
          <span className="filter-label">Phase</span>
          <div className="chips">
            {PHASES.map((p) => (
              <button
                key={p}
                className={`chip ${filter.phases.includes(p) ? 'on' : ''}`}
                onClick={() => togglePhase(p)}
              >
                {titleCase(p)}
              </button>
            ))}
          </div>
        </div>

        <div className="filter-row" data-hidden={source !== 'search' || undefined}>
          <span className="filter-label">
            <label htmlFor="theme">Theme</label>
          </span>
          <select
            id="theme"
            className="theme-select"
            value={filter.theme}
            onChange={(e) => apply({ theme: e.target.value })}
          >
            <option value="">Any theme</option>
            {COMMON_THEMES.map((t) => (
              <option key={t} value={t}>
                {titleCase(t)}
              </option>
            ))}
          </select>
        </div>
      </section>

      {phase === 'empty' ? (
        <section className="puzzle-empty">
          <h2>
            {source === 'search' ? 'Nothing matches that' : 'Nothing here yet'}
          </h2>
          <p>
            {error ||
              (source === 'search'
                ? 'No puzzle has all of those at once. Widen the rating, or drop the theme.'
                : source === 'wrong'
                  ? 'Puzzles you get wrong land here, and leave once you get them right.'
                  : 'Save a puzzle with the star and it waits here for you.')}
          </p>
        </section>
      ) : (
        <div className="puzzle-body">
          <div className="board-wrap">
            <div className="frame">
              <Chessboard
                position={board ? board.fen() : 'start'}
                onPieceDrop={(f, t) => tryMove(f, t)}
                onSquareClick={onSquareClick}
                boardOrientation={puzzle?.color || 'white'}
                arePiecesDraggable={phase === 'solving'}
                customBoardStyle={{ borderRadius: '6px' }}
                customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
                customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
                customSquareStyles={squareStyles}
              />
            </div>
          </div>

          <aside className="panel">
            <div className={`state ${phase === 'failed' ? 'wrong' : 'yours'}`}>
              {phase === 'loading' && <span className="head">Finding a puzzle</span>}
              {phase === 'setup' && <span className="head">Watch the blunder</span>}
              {phase === 'solving' && (
                <>
                  <span className="head">
                    {puzzle.color === 'white' ? 'White' : 'Black'} to play
                  </span>
                  {hint && <span className="hint">{hint.text}</span>}
                </>
              )}
              {phase === 'solved' && <span className="head">Solved</span>}
              {phase === 'failed' && <span className="head">Missed</span>}
            </div>

            {puzzle && (
              <div className="record">
                <div className="record-head">
                  <h3>This puzzle</h3>
                  <button
                    className={`star ${saved ? 'on' : ''}`}
                    onClick={toggleSave}
                    title={saved ? 'Saved' : 'Save this puzzle'}
                    aria-label={saved ? 'Saved' : 'Save this puzzle'}
                  >
                    {saved ? '★' : '☆'}
                  </button>
                </div>
                <dl className="meta">
                  <div>
                    <dt>Rating</dt>
                    <dd>{puzzle.rating}</dd>
                  </div>
                  <div>
                    <dt>Length</dt>
                    <dd>{puzzle.moves === 1 ? '1 move' : `${puzzle.moves} moves`}</dd>
                  </div>
                  <div>
                    <dt>Phase</dt>
                    <dd>{titleCase(puzzle.phase)}</dd>
                  </div>
                </dl>
                {(phase === 'solved' || phase === 'failed') && puzzle.explanation && (
                  <div className="explain">
                    <p className="headline">{puzzle.explanation.headline}</p>
                    {(puzzle.explanation.points || []).map((pt) => (
                      <p key={pt} className="point">
                        {pt}
                      </p>
                    ))}
                  </div>
                )}
                {(phase === 'solved' || phase === 'failed') &&
                  puzzle.themes.some((t) => COMMON_THEMES.includes(t)) && (
                    <p className="themes">
                      {puzzle.themes
                        .filter((t) => COMMON_THEMES.includes(t))
                        .map(titleCase)
                        .join(' · ')}
                    </p>
                  )}
              </div>
            )}

            {(phase === 'solved' || phase === 'failed') && (
              <div className="after">
                <button className="wide" onClick={next}>
                  Next puzzle
                </button>
                <button className="ghost wide" onClick={anotherLikeThis}>
                  Another like this
                </button>
                {puzzle.game_url && (
                  <a className="source" href={puzzle.game_url} target="_blank" rel="noreferrer">
                    The game it came from
                  </a>
                )}
              </div>
            )}

            {phase === 'solving' && (
              <div className="during">
                <button
                  className="wide"
                  disabled={hints >= 3}
                  onClick={() => setHints((h) => Math.min(3, h + 1))}
                >
                  {hints === 0 ? 'Hint' : hints < 3 ? 'Another hint' : 'No more hints'}
                </button>
                <button className="ghost wide" onClick={showSolution}>
                  Show solution
                </button>
                <button className="ghost wide" onClick={next}>
                  Skip
                </button>
              </div>
            )}
          </aside>
        </div>
      )}
    </main>
  )
}
