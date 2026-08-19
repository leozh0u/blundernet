import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import SearchTree from './SearchTree.jsx'
import Account from './Account.jsx'
import Puzzles from './Puzzles.jsx'
import Ranked from './Ranked.jsx'
import Profile from './Profile.jsx'
import Streak from './Streak.jsx'
import BoardOverlay from './BoardOverlay.jsx'
import Logo from './Logo.jsx'

const api = {
  async createGame(color, mode, level) {
    const res = await fetch('/api/games', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ color, mode, level }),
    })
    if (!res.ok) throw new Error('The game would not start. Try again.')
    return res.json()
  },
  async join(id) {
    const res = await fetch(`/api/games/${id}/join`, { method: 'POST' })
    if (!res.ok) throw new Error('That game is full or gone.')
    return res.json()
  },
  async game(id) {
    const res = await fetch(`/api/games/${id}`)
    if (!res.ok) throw new Error('No such game.')
    return res.json()
  },
  async hint(id) {
    await fetch(`/api/games/${id}/hint`, { method: 'POST' })
  },
  async profile() {
    const res = await fetch('/api/me/profile')
    return res.ok ? res.json() : null
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
  // The review is engine work, so it is asked for and then waited on. The
  // worker stores it, which is why a reload can pick it up rather than paying
  // for the same evaluations twice.
  async review(id) {
    await fetch(`/api/games/${id}/review`, { method: 'POST' })
    for (let i = 0; i < 40; i++) {
      const res = await fetch(`/api/games/${id}/review`)
      if (res.status === 200) return res.json()
      if (res.status !== 202) return null
      await new Promise((r) => setTimeout(r, 700))
    }
    return null
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
    return { kind: 'draw', title: 'Draw' }
  }
  const won =
    (state.result === '1-0' && state.player_color === 'white') ||
    (state.result === '0-1' && state.player_color === 'black')
  return won ? { kind: 'win', title: 'You won' } : { kind: 'loss', title: 'You lost' }
}

// Three pages, and no router library for three pages. The path is read on
// load and pushed on a click, so every page is linkable and the back button
// works. The server already serves index.html for any path.
//
// Puzzles are the front page. Playing the engine is one model against one
// opponent; the puzzle side is six million positions with a search over them,
// and it is the part somebody would come back to.
// /play/<id> is a friend game somebody shared.
function sharedGameID(path) {
  const m = path.match(/^\/play\/([-a-f0-9]{8,})$/)
  return m ? m[1] : null
}

function routeOf(path) {
  if (path.startsWith('/puzzles/streak')) return 'streak'
  if (path.startsWith('/puzzles/ranked')) return 'ranked'
  if (path.startsWith('/play')) return 'play'
  if (path.startsWith('/me')) return 'me'
  return 'puzzles'
}

// /puzzles/<id> opens one puzzle by itself, which is what a shared link is.
function sharedPuzzleID(path) {
  const m = path.match(/^\/puzzles\/([A-Za-z0-9]+)$/)
  if (!m || m[1] === 'ranked' || m[1] === 'streak') return null
  return m[1]
}

const PATHS = {
  puzzles: '/',
  ranked: '/puzzles/ranked',
  streak: '/puzzles/streak',
  play: '/play',
  me: '/me',
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
  // Learning or rated, and the level the learning game is played at. Rated
  // games take the level from the ladder, so the picker is hidden there.
  const [mode, setMode] = useState('rated')
  const [level, setLevel] = useState(3)
  const [ladder, setLadder] = useState(null)
  const [recent, setRecent] = useState([])
  const [mine, setMine] = useState(null)
  const [hint, setHint] = useState(null)
  const [review, setReview] = useState(null)
  const [reviewing, setReviewing] = useState(false)
  // The side this browser plays. In a friend game it is not player_color,
  // which names the side of whoever made the game.
  const [myColor, setMyColor] = useState('white')
  const wsRef = useRef(null)

  useEffect(() => {
    api.stats().then(setStats).catch(() => {})
    api.profile()
      .then((p) => {
        if (!p) return
        setLadder(p.bot_level)
        setMine(p)
      })
      .catch(() => {})
    fetch('/api/me/games?limit=5')
      .then((r) => (r.ok ? r.json() : []))
      .then((g) => setRecent(Array.isArray(g) ? g : g?.games || []))
      .catch(() => {})
  }, [state?.status])

  useEffect(() => () => wsRef.current?.close(), [])

  // Opening a shared game link takes the second seat, if it is still free.
  useEffect(() => {
    const id = sharedGameID(window.location.pathname)
    if (!id) return
    api
      .join(id)
      .then((st) => {
        setState(st)
        setMyColor(st.you || st.player_color)
        connect(st.id)
      })
      .catch(() =>
        api
          .game(id)
          .then((st) => {
            setState(st)
            setMyColor(st.you || 'white')
            connect(st.id)
          })
          .catch((e) => setError(e.message)),
      )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const onPop = () => setRoute(routeOf(window.location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const go = (to) => {
    window.history.pushState({}, '', PATHS[to])
    setRoute(to)
    // Clicking Play while a game is on screen means "take me back to the
    // menu". Without this the nav looks broken: the path changes and the
    // board stays, with no way back except a reload.
    if (to === 'play') {
      setState(null)
      setHint(null)
    }
  }

  const connect = useCallback((id) => {
    wsRef.current?.close()
    const ws = new WebSocket(wsURL(id))
    ws.onmessage = (ev) => {
      const next = JSON.parse(ev.data)
      // A hint is an answer to a question, not a new position. It arrives on
      // the same socket because the search takes as long as an engine move.
      if (next.type === 'hint') {
        setHint({ from: next.uci.slice(0, 2), to: next.uci.slice(2, 4) })
        return
      }
      setHint(null)
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
    setHint(null)
    setReview(null)
    try {
      const st = await api.createGame(color, mode, level)
      setState(st)
      setMyColor(st.you || st.player_color)
      if (st.friend) {
        window.history.pushState({}, '', `/play/${st.id}`)
      }
      connect(st.id)
    } catch (e) {
      setError(e.message)
    }
  }

  const myTurn =
    state && state.status === 'ongoing' && state.turn === myColor && !state.waiting

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
    const mine = piece && piece.color === (myColor === 'white' ? 'w' : 'b')
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
        styles[sq] = { background: 'rgba(246, 200, 92, 0.45)' }
      }
    }
    if (selected) {
      styles[selected] = { background: 'rgba(246, 200, 92, 0.6)' }
    }
    for (const sq of legalTargets) {
      styles[sq] = {
        ...styles[sq],
        background:
          'radial-gradient(circle, rgba(16,28,40,0.42) 20%, transparent 22%)',
      }
    }
    return styles
  }, [selected, legalTargets, state, hint])

  // The invite is a link somebody has to paste into a message, so it is shown
  // as text they can select, with a button for the common case. Telling them
  // to "share this page" and leaving the URL in the address bar is not a
  // feature, it is a shrug.
  const inviteURL = state?.friend ? `${window.location.origin}/play/${state.id}` : ''
  const [invited, setInvited] = useState(false)
  const copyInvite = async () => {
    try {
      await navigator.clipboard.writeText(inviteURL)
      setInvited(true)
      setTimeout(() => setInvited(false), 1600)
    } catch {
      window.prompt('Copy this link', inviteURL)
    }
  }

  const askForReview = async () => {
    setReviewing(true)
    setReview(await api.review(state.id))
    setReviewing(false)
  }

  const result = state ? outcome(state) : null
  const pairs = state ? movePairs(state.moves) : []

  return (
    <div className="page">
      <header className="bar">
        <button className="brand" onClick={() => go('puzzles')}>
          <Logo size={30} />
          <span className="word">BlunderNet</span>
        </button>
        <nav className="nav">
          <button
            className={route === 'play' ? '' : 'on'}
            onClick={() => go('puzzles')}
          >
            Puzzles
          </button>
          <button className={route === 'play' ? 'on' : ''} onClick={() => go('play')}>
            Play
          </button>
        </nav>
        <Account refreshKey={ratingKey} />
      </header>

      {route === 'me' ? (
        <Profile
          onDrill={(list) => {
            window.history.pushState({}, '', `/?drill=${list}`)
            setRoute('puzzles')
          }}
        />
      ) : route !== 'play' ? (
        <>
          <div className="pagehead">
            <h1>Puzzles</h1>
          </div>
          <div className="modes" role="tablist">
            <button
              role="tab"
              aria-selected={route === 'puzzles'}
              className={route === 'puzzles' ? 'on' : ''}
              onClick={() => go('puzzles')}
            >
              Learning
            </button>
            <button
              role="tab"
              aria-selected={route === 'ranked'}
              className={route === 'ranked' ? 'on' : ''}
              onClick={() => go('ranked')}
            >
              Ranked
            </button>
            <button
              role="tab"
              aria-selected={route === 'streak'}
              className={route === 'streak' ? 'on' : ''}
              onClick={() => go('streak')}
            >
              Streak
            </button>
          </div>
          {route === 'streak' ? (
            <Streak />
          ) : route === 'ranked' ? (
            <Ranked />
          ) : (
            <Puzzles shared={sharedPuzzleID(window.location.pathname)} />
          )}
        </>
      ) : !state ? (
        <section className="lobby">
          <div className="pagehead">
            <h1>{mode === 'friend' ? 'Play a friend' : 'Play the bot'}</h1>
          </div>
          <div className="modes" role="tablist">
            <button
              role="tab"
              aria-selected={mode === 'friend'}
              className={mode === 'friend' ? 'on' : ''}
              onClick={() => setMode('friend')}
            >
              A friend
            </button>
            <button
              role="tab"
              aria-selected={mode === 'learning'}
              className={mode === 'learning' ? 'on' : ''}
              onClick={() => setMode('learning')}
            >
              Learning
            </button>
            <button
              role="tab"
              aria-selected={mode === 'rated'}
              className={mode === 'rated' ? 'on' : ''}
              onClick={() => setMode('rated')}
            >
              Rated
            </button>
          </div>

          <div className="levels">
            {mode === 'friend' ? (
              <span className="ladder">
                Pick a side <i>you get a link to send</i>
              </span>
            ) : mode === 'learning' ? (
              <>
                <span className="filter-label">Level</span>
                <div className="chips">
                  {[1, 2, 3, 4, 5, 6].map((n) => (
                    <button
                      key={n}
                      className={`chip ${level === n ? 'on' : ''}`}
                      onClick={() => setLevel(n)}
                    >
                      {n}
                    </button>
                  ))}
                </div>
              </>
            ) : (
              <>
                <span className="filter-label">Level</span>
                <span className="ladder">
                  {ladder || 3} <i>moves with your results</i>
                </span>
              </>
            )}
          </div>
          {/* Both of these start a game, so they are labelled as the choice
              they are. Without the heading the pair reads as a setting that
              has already been made rather than a question being asked. */}
          <span className="choices-label">Play as</span>
          <div className="choices">
            <button className="choice light" onClick={() => newGame('white')}>
              <span className="seal light">♔</span>
              <span className="label">White</span>
            </button>
            <button className="choice dark" onClick={() => newGame('black')}>
              <span className="seal dark">♚</span>
              <span className="label">Black</span>
            </button>
          </div>
          <div className="cards">
            <div className="card">
              <h2>You</h2>
              <dl className="meta">
                <div>
                  <dt>Rating</dt>
                  <dd>{mine ? Math.round(mine.rating) : '...'}</dd>
                </div>
                <div>
                  <dt>Bot level</dt>
                  <dd>{ladder || 3}</dd>
                </div>
                <div>
                  <dt>Rated games</dt>
                  <dd>{mine ? mine.rated_games : '...'}</dd>
                </div>
              </dl>
            </div>

            <div className="card">
              <h2>Last games</h2>
              {recent.length === 0 ? (
                <p className="explains">Nothing finished yet.</p>
              ) : (
                <table className="history">
                  <tbody>
                    {recent.map((g) => (
                      <tr key={g.id}>
                        <td>{g.player_color === 'white' ? 'White' : 'Black'}</td>
                        <td>{outcome({ ...g, status: 'finished' })?.title || ''}</td>
                        <td className="num">{g.ply} plies</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          {stats && stats.total > 0 && (
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
        </section>
      ) : (
        <main className="game">
          <div className="board-wrap">
            <div className="frame">
              <Chessboard
                position={state.fen}
                onPieceDrop={(f, t) => tryMove(f, t)}
                onSquareClick={onSquareClick}
                boardOrientation={myColor}
                arePiecesDraggable={state.status === 'ongoing'}
                customBoardStyle={{ borderRadius: '6px' }}
                customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
                customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
                customSquareStyles={squareStyles}
              />
              <BoardOverlay
                orientation={myColor}
                glow={hint?.from}
                arrow={hint}
              />
              {result && (
                <div className="overlay">
                  <div className={`verdict ${result.kind}`}>
                    <div className="wax">♞</div>
                    <h2>{result.title}</h2>
                    <p className="cause">{state.termination}</p>
                    <div className="again">
                      <button onClick={() => newGame('white')}>Again as White</button>
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
              {state.waiting ? (
                <span className="head">Waiting for your friend</span>
              ) : state.status === 'finished' ? (
                <span className="head">The game is done</span>
              ) : myTurn ? (
                <span className="head">Your move</span>
              ) : (
                state.friend ? (
                  <span className="head">Their move</span>
                ) : (
                  <>
                    <span className="head">Thinking</span>
                    <SearchTree />
                  </>
                )
              )}
            </div>

            {state.status === 'finished' && (
              <div className="record">
                <h3>Review</h3>
                {!review ? (
                  <div className="after">
                    <button className="wide" disabled={reviewing} onClick={askForReview}>
                      {reviewing ? 'Looking at your moves' : 'Review this game'}
                    </button>
                  </div>
                ) : review.worst?.length ? (
                  <ul className="worst">
                    {review.worst.map((m) => (
                      <li key={m.ply}>
                        <button onClick={() => setState((st) => ({ ...st, fen: m.fen }))}>
                          <span className="san">
                            {Math.ceil(m.ply / 2)}. {m.san}
                          </span>
                          <span className="loss">
                            {m.material <= -1
                              ? `${m.material.toFixed(0)} material`
                              : `-${m.loss.toFixed(2)}`}
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="empty">Nothing stands out. No move cost much.</p>
                )}
              </div>
            )}

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

            {state.waiting && (
              <div className="invite">
                <label htmlFor="invite">Send them this link</label>
                <input id="invite" readOnly value={inviteURL} onFocus={(e) => e.target.select()} />
                <button className="wide" onClick={copyInvite}>
                  {invited ? 'Copied' : 'Copy link'}
                </button>
              </div>
            )}

            {state.status === 'ongoing' && (
              <div className="during">
                {!state.rated && (
                  <button
                    className="wide"
                    disabled={!myTurn}
                    onClick={() => api.hint(state.id)}
                  >
                    Hint
                  </button>
                )}
                <button className="ghost wide" onClick={() => api.resign(state.id)}>
                  Resign
                </button>
              </div>
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
