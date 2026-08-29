import { useCallback, useEffect, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { useBoardWidth } from './board.js'
import { PositionText } from './Position.jsx'

// Reviewing a game played anywhere.
//
// Paste it, wait a few seconds, walk it. No account, because this is the most
// useful thing the site does for somebody who arrived from a link and putting
// a signup in front of it means they never find out.

// "1 blunders" is the kind of thing that makes a site look unfinished.
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`

const LABELS = {
  best: 'Best',
  excellent: 'Excellent',
  good: 'Good',
  inaccuracy: 'Inaccuracy',
  mistake: 'Mistake',
  blunder: 'Blunder',
}

// Polling rather than a socket. A review takes a handful of seconds and then
// never changes, so the simplest thing that works is the right thing.
const POLL_MS = 1200

export default function Analyse() {
  const [frame, boardWidth] = useBoardWidth()
  const [pgn, setPgn] = useState('')
  const [state, setState] = useState('idle') // idle, working, done, failed
  const [error, setError] = useState('')
  const [result, setResult] = useState(null)
  const [cursor, setCursor] = useState(0)
  const polling = useRef(null)

  useEffect(() => () => clearInterval(polling.current), [])

  const poll = useCallback((id) => {
    clearInterval(polling.current)
    polling.current = setInterval(async () => {
      const res = await fetch(`/api/review/${id}`, { credentials: 'same-origin' })
      if (res.status === 202) return // still working
      clearInterval(polling.current)
      if (!res.ok) {
        setState('failed')
        setError('That review could not be finished.')
        return
      }
      setResult(await res.json())
      setCursor(0)
      setState('done')
    }, POLL_MS)
  }, [])

  const submit = async (e) => {
    e.preventDefault()
    setError('')
    setResult(null)
    setState('working')
    const res = await fetch('/api/review/pgn', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pgn }),
    })
    const body = await res.json().catch(() => ({}))
    if (!res.ok) {
      setState('failed')
      setError(body.error || 'That could not be read as a game.')
      return
    }
    poll(body.id)
  }

  const moves = result?.moves || []
  // Cursor 0 is the starting position; move n is shown after it was played.
  const shown = cursor === 0 ? null : moves[cursor - 1]
  // Only so the position can be read out. The board itself is driven by the
  // FEN directly, since nothing here needs the rules.
  const readable = shown ? new Chess(shown.fen) : new Chess()

  return (
    <section className="analyse">
      <form className="analyse-paste" onSubmit={submit}>
        <label htmlFor="pgn">Paste a game</label>
        <textarea
          id="pgn"
          value={pgn}
          onChange={(e) => setPgn(e.target.value)}
          rows={5}
          placeholder="1. e4 e5 2. Nf3 Nc6 ..."
        />
        <div className="analyse-actions">
          <button disabled={!pgn.trim() || state === 'working'}>
            {state === 'working' ? 'Reading the game' : 'Review it'}
          </button>
          <span className="analyse-hint">
            Works with anything copied from Lichess or Chess.com.
          </span>
        </div>
      </form>

      {error && <p className="rooms-error">{error}</p>}

      {result && (
        <>
          <div className="analyse-scores">
            <div>
              <span className="analyse-side">White</span>
              <strong>{result.white_accuracy}%</strong>
              <span className="analyse-counts">
                {plural(result.white.blunder, 'blunder', 'blunders')} ·{' '}
                {plural(result.white.mistake, 'mistake', 'mistakes')} ·{' '}
                {plural(result.white.inaccuracy, 'inaccuracy', 'inaccuracies')}
              </span>
            </div>
            <div>
              <span className="analyse-side">Black</span>
              <strong>{result.black_accuracy}%</strong>
              <span className="analyse-counts">
                {plural(result.black.blunder, 'blunder', 'blunders')} ·{' '}
                {plural(result.black.mistake, 'mistake', 'mistakes')} ·{' '}
                {plural(result.black.inaccuracy, 'inaccuracy', 'inaccuracies')}
              </span>
            </div>
          </div>

          <div className="analyse-body">
            <div className="analyse-board" ref={frame}>
              {boardWidth > 0 && (
                <Chessboard
                  id="Analyse"
                  boardWidth={boardWidth}
                  position={shown ? shown.fen : 'start'}
                  arePiecesDraggable={false}
                  animationDuration={0}
                  customBoardStyle={{ borderRadius: '6px' }}
                  customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
                  customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
                />
              )}
              <PositionText board={readable} label="Position after this move" />
              {shown && (
                <p className="analyse-said">
                  <span className={`tag j-${shown.judgement}`}>{LABELS[shown.judgement]}</span>{' '}
                  {shown.san} took your chances from {shown.win_before}% to {shown.win_after}%.
                  {shown.better_san && <> The engine wanted {shown.better_san}.</>}
                </p>
              )}
            </div>

            <ol className="analyse-moves">
              <li>
                <button className={cursor === 0 ? 'on' : ''} onClick={() => setCursor(0)}>
                  Start
                </button>
              </li>
              {moves.map((m, i) => (
                <li key={m.ply}>
                  <button
                    className={`${cursor === i + 1 ? 'on' : ''} j-${m.judgement}`}
                    onClick={() => setCursor(i + 1)}
                  >
                    <span className="analyse-no">
                      {Math.floor(i / 2) + 1}
                      {m.white ? '.' : '...'}
                    </span>
                    <span className="analyse-san">{m.san}</span>
                    <span className="analyse-mark">{LABELS[m.judgement]}</span>
                  </button>
                </li>
              ))}
            </ol>
          </div>
        </>
      )}
    </section>
  )
}
