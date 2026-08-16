import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import SearchTree from './SearchTree.jsx'
import Account from './Account.jsx'
import Puzzles from './Puzzles.jsx'

const api = {
  async createGame(color) {
    const res = await fetch('/api/games', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ color }),
    })
    if (!res.ok) throw new Error('The cabinet would not open. Try again.')
    return res.json()
  },
  async move(id, uci) {
    const res = await fetch(`/api/games/${id}/moves`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uci }),
    })
    return { ok: res.ok, body: await res.json() }
  },
  async resign(id) {
    await fetch(`/api/games/${id}/resign`, { method: 'POST' })
  },
  async stats() {
    const res = await fetch('/api/stats')
    return res.ok ? res.json() : null
  },
}

function wsURL(id) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${window.location.host}/api/games/${id}/ws`
}

const other = (c) => (c === 'white' ? 'black' : 'white')

// The API speaks UCI; players read algebraic. Replay the game to convert.
function movePairs(moves) {
  const board = new Chess()
  const san = moves.map((uci) => {
    const mv = board.move({
      from: uci.slice(0, 2),
      to: uci.slice(2, 4),
      promotion: uci[4] || undefined,
    })
    return mv ? mv.san : uci
  })
  const pairs = []
  for (let i = 0; i < san.length; i += 2) {
    pairs.push({ n: i / 2 + 1, white: san[i], black: san[i + 1] })
  }
  return pairs
}

function outcome(state) {
  if (state.status !== 'finished') return null
  if (state.result === '1/2-1/2') {
    return { kind: 'draw', title: 'A draw', flavour: 'Honours even.' }
  }
  const won =
    (state.result === '1-0' && state.player_color === 'white') ||
    (state.result === '0-1' && state.player_color === 'black')
  return won
    ? { kind: 'win', title: 'Victory', flavour: 'The automaton is beaten.' }
    : { kind: 'loss', title: 'Defeat', flavour: 'The automaton prevails.' }
}

// Two pages, and no router library for two pages. The path is read on load
// and pushed on a click, so both are linkable and the back button works. The
// server already serves index.html for any path.
function routeOf(path) {
  return path.startsWith('/puzzles') ? 'puzzles' : 'play'
}

export default function App() {
  const [route, setRoute] = useState(() => routeOf(window.location.pathname))
  const [state, setState] = useState(null)
  // Bumped when a game reaches a terminal state, which is the only moment the
  // rating can have moved. Polling the profile on a timer would be busywork.
  const [ratingKey, setRatingKey] = useState(0)
  const [stats, setStats] = useState(null)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState(null)
  const wsRef = useRef(null)

  useEffect(() => {
    api.stats().then(setStats).catch(() => {})
  }, [state?.status])

  useEffect(() => () => wsRef.current?.close(), [])

  useEffect(() => {
    const onPop = () => setRoute(routeOf(window.location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const go = (to) => {
    window.history.pushState({}, '', to === 'puzzles' ? '/puzzles' : '/')
    setRoute(to)
  }

  const connect = useCallback((id) => {
    wsRef.current?.close()
    const ws = new WebSocket(wsURL(id))
    ws.onmessage = (ev) => {
      const next = JSON.parse(ev.data)
      setState((prev) => {
        if (next.status === 'finished' && prev?.status !== 'finished') {
          // The rating is written by whichever service archives the game, so
          // give that write a moment to land before asking for the new number.
          setTimeout(() => setRatingKey((k) => k + 1), 600)
        }
        return next
      })
    }
    ws.onerror = () => setError('The connection was lost. Refresh to resume.')
    wsRef.current = ws
  }, [])

  const newGame = async (color) => {
    setError('')
    setSelected(null)
    try {
      const st = await api.createGame(color)
      setState(st)
      connect(st.id)
    } catch (e) {
      setError(e.message)
    }
  }

  const myTurn = state && state.status === 'ongoing' && state.turn === state.player_color

  const tryMove = (from, to) => {
    if (!myTurn) return false
    // The server is the authority. chess.js here only spots promotions and
    // rejects hopeless drops without a round trip.
    const probe = new Chess(state.fen)
    let mv
    try {
      mv = probe.move({ from, to, promotion: 'q' })
    } catch {
      return false
    }
    if (!mv) return false
    setSelected(null)
    setState({ ...state, fen: probe.fen(), turn: other(state.turn) })
    api.move(state.id, from + to + (mv.promotion ? 'q' : '')).then(({ ok, body }) => {
      if (!ok) {
        setError(body.error || 'That move was refused.')
        setState((s) => ({ ...s }))
      }
    })
    return true
  }

  const onSquareClick = (square) => {
    if (!myTurn) return
    if (selected === square) return setSelected(null)
    if (selected && tryMove(selected, square)) return
    const piece = new Chess(state.fen).get(square)
    const mine = piece && piece.color === (state.player_color === 'white' ? 'w' : 'b')
    setSelected(mine ? square : null)
  }

  const legalTargets = useMemo(() => {
    if (!selected || !state) return []
    return new Chess(state.fen)
      .moves({ square: selected, verbose: true })
      .map((m) => m.to)
  }, [selected, state])

  const squareStyles = useMemo(() => {
    const styles = {}
    const last = state?.moves?.[state.moves.length - 1]
    if (last) {
      for (const sq of [last.slice(0, 2), last.slice(2, 4)]) {
        styles[sq] = { background: 'rgba(203, 150, 60, 0.38)' }
      }
    }
    if (selected) {
      styles[selected] = { background: 'rgba(203, 150, 60, 0.55)' }
    }
    for (const sq of legalTargets) {
      styles[sq] = {
        ...styles[sq],
        background:
          'radial-gradient(circle, rgba(78,58,34,0.45) 20%, transparent 22%)',
      }
    }
    return styles
  }, [selected, legalTargets, state])

  const result = state ? outcome(state) : null
  const pairs = state ? movePairs(state.moves) : []

  return (
    <div className="page">
      <Account refreshKey={ratingKey} />
      <header className="masthead">
        <div className="crest">
          <span className="knight">♞</span>
        </div>
        <div className="titles">
          <h1>BlunderNet</h1>
          <p className="rule">
            <span>Play a neural network</span>
          </p>
        </div>
        <nav className="nav">
          <button className={route === 'play' ? 'on' : ''} onClick={() => go('play')}>
            Play
          </button>
          <button className={route === 'puzzles' ? 'on' : ''} onClick={() => go('puzzles')}>
            Puzzles
          </button>
        </nav>
        {route === 'play' && stats && stats.total > 0 && (
          <dl className="ledger">
            <div>
              <dt>Games</dt>
              <dd>{stats.total}</dd>
            </div>
            <div>
              <dt>Engine</dt>
              <dd>{stats.engine_wins}</dd>
            </div>
            <div>
              <dt>Players</dt>
              <dd>{stats.player_wins}</dd>
            </div>
            <div>
              <dt>Draws</dt>
              <dd>{stats.draws}</dd>
            </div>
          </dl>
        )}
      </header>

      {route === 'puzzles' ? (
        <Puzzles />
      ) : !state ? (
        <section className="lobby">
          <h2>Play the engine</h2>
          <p>
            A neural network trained on games from strong Lichess players,
            picking each reply by searching a few hundred positions ahead. It
            plays around 1000 Elo, so it is beatable. Your rating is tracked
            whether or not you make an account.
          </p>
          <div className="choices">
            <button className="choice light" onClick={() => newGame('white')}>
              <span className="seal light">♔</span>
              <span className="label">Play White</span>
              <span className="sub">Yours is the first move</span>
            </button>
            <button className="choice dark" onClick={() => newGame('black')}>
              <span className="seal dark">♚</span>
              <span className="label">Play Black</span>
              <span className="sub">The machine opens</span>
            </button>
          </div>
          <p className="fineprint">
            300 simulations per move · policy and value heads · 2.6M parameters
          </p>
        </section>
      ) : (
        <main className="game">
          <div className="board-wrap">
            <div className="frame">
              <Chessboard
                position={state.fen}
                onPieceDrop={(f, t) => tryMove(f, t)}
                onSquareClick={onSquareClick}
                boardOrientation={state.player_color}
                arePiecesDraggable={state.status === 'ongoing'}
                customBoardStyle={{ borderRadius: '6px' }}
                customDarkSquareStyle={{ backgroundColor: '#769656' }}
                customLightSquareStyle={{ backgroundColor: '#eeeed2' }}
                customSquareStyles={squareStyles}
              />
              {result && (
                <div className="overlay">
                  <div className={`verdict ${result.kind}`}>
                    <div className="wax">♞</div>
                    <h2>{result.title}</h2>
                    <p className="flavour">{result.flavour}</p>
                    <p className="cause">{state.termination}</p>
                    <div className="again">
                      <button onClick={() => newGame('white')}>Play again as White</button>
                      <button className="ghost" onClick={() => newGame('black')}>
                        as Black
                      </button>
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>

          <aside className="panel">
            <div className={`state ${myTurn ? 'yours' : 'machine'}`}>
              {state.status === 'finished' ? (
                <span className="head">The game is done</span>
              ) : myTurn ? (
                <>
                  <span className="head">Your move</span>
                  <span className="hint">Drag a piece, or tap it and its square.</span>
                </>
              ) : (
                <>
                  <span className="head">The automaton deliberates</span>
                  <SearchTree />
                  <span className="hint mono">searching · 300 sims</span>
                </>
              )}
            </div>

            <div className="record">
              <h3>Record of play</h3>
              {pairs.length === 0 ? (
                <p className="empty">Not a move yet.</p>
              ) : (
                <ol>
                  {pairs.map((p) => (
                    <li key={p.n}>
                      <span className="num">{p.n}.</span>
                      <span className="san">{p.white}</span>
                      <span className="san">{p.black || '…'}</span>
                    </li>
                  ))}
                </ol>
              )}
            </div>

            {state.status === 'ongoing' && (
              <button className="ghost wide" onClick={() => api.resign(state.id)}>
                Concede
              </button>
            )}
          </aside>
        </main>
      )}

      {error && <div className="error">{error}</div>}

      <footer className="foot">
        <a href="https://github.com/leozh0u/blundernet">Source</a>
        <span aria-hidden="true"> · </span>
        <a href="https://github.com/leozh0u/blundernet-engine">The engine</a>
        <span aria-hidden="true"> · </span>
        <a href="/status">Status</a>
        <span aria-hidden="true"> · </span>
        <a href="/privacy">Privacy</a>
        <span aria-hidden="true"> · </span>
        <a href="/terms">Terms</a>
      </footer>
    </div>
  )
}
