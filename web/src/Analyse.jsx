import { useCallback, useEffect, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { useBoardWidth } from './board.js'
import { PositionText } from './Position.jsx'
import BoardOverlay from './BoardOverlay.jsx'
import { CLASS, ORDER, labelOf, markOf, notable } from './judgements.js'

// Reviewing a game played anywhere.
//
// Paste it, wait a few seconds, walk it. No account, because this is the most
// useful thing the site does for somebody who arrived from a link and putting
// a signup in front of it means they never find out.

// How the position stands for White, which is the side an evaluation bar is
// always drawn from. The review scores every move from the mover's point of
// view, so Black's moves have to be turned around before they can share a bar.
const whiteChances = (m) => (m ? (m.white ? m.win_after : 100 - m.win_after) : 50)

// Polling rather than a socket. A review takes a handful of seconds and then
// never changes, so the simplest thing that works is the right thing.
const POLL_MS = 1200

// Polling has to stop on its own. A worker that died leaves the review never
// finishing, and a page left open on a phone would ask forever. The worker's
// own budget is twenty seconds, so a minute is past any answer that is coming.
const GIVE_UP_AFTER = 60_000

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
    const startedAt = Date.now()
    polling.current = setInterval(async () => {
      if (Date.now() - startedAt > GIVE_UP_AFTER) {
        clearInterval(polling.current)
        setState('failed')
        setError('That review is taking too long. Try again in a moment.')
        return
      }
      let res
      try {
        res = await fetch(`/api/review/${id}`, { credentials: 'same-origin' })
      } catch {
        return // a dropped request is not a failed review; the next tick retries
      }
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
    let res
    try {
      res = await fetch('/api/review/pgn', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pgn }),
      })
    } catch {
      setState('failed')
      setError('That could not be sent. Check your connection and try again.')
      return
    }
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
          {/* Both sides in one table rather than two cards side by side, so
              every row is a direct comparison: you read across to see who made
              the mistakes rather than holding two lists in your head. Classes
              nobody achieved are dropped, since a row of zeroes is noise. */}
          <table className="analyse-scores">
            <thead>
              <tr>
                <th />
                <th>White</th>
                <th>Black</th>
              </tr>
            </thead>
            <tbody>
              <tr className="analyse-accuracy">
                <th scope="row">Accuracy</th>
                <td>{result.white_accuracy}%</td>
                <td>{result.black_accuracy}%</td>
              </tr>
              {ORDER.filter((k) => result.white[k] > 0 || result.black[k] > 0).map((k) => (
                <tr key={k}>
                  <th scope="row">
                    <span className="j-label">
                      <span className={`pip j-${k}`}>{CLASS[k].mark}</span>
                      {CLASS[k].label}
                    </span>
                  </th>
                  <td>{result.white[k]}</td>
                  <td>{result.black[k]}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <div className="analyse-body">
            <div className="analyse-board" ref={frame}>
              {/* The overlay is positioned against this box rather than the
                  column, so it has to be exactly the board and nothing else.
                  Sized to the board, the badge lands on its square; sized to
                  the column it floats over the text underneath. */}
              <div
                className="analyse-frame"
                style={{ width: boardWidth || undefined, height: boardWidth || undefined }}
              >
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
                {/* Only for the moves worth stopping at. A badge on every
                    move turns the board into a rash. */}
                {shown && notable(shown.judgement) && (
                  <BoardOverlay
                    verdict={{
                      square: shown.uci.slice(2, 4),
                      judgement: shown.judgement,
                      mark: markOf(shown.judgement),
                    }}
                  />
                )}
              </div>
              <PositionText board={readable} label="Position after this move" />

              {/* Who is winning, as one bar. A percentage in a sentence is a
                  number to decode; a bar is the shape of the game. */}
              <div
                className="evalbar"
                role="img"
                aria-label={`White has ${Math.round(whiteChances(shown))}% of the chances`}
              >
                <div className="evalbar-white" style={{ width: `${whiteChances(shown)}%` }} />
              </div>

              {/* Stated as a change rather than narrated. "Took the chances
                  from" only reads correctly on a move that lost something, and
                  putting it under a move labelled Brilliant said the opposite
                  of what the label said. The numbers are the mover's own
                  chances, so the side is named to stop that being a guess. */}
              {shown ? (
                <p className="analyse-said">
                  <span className={`tag j-${shown.judgement}`}>
                    <span className="tag-mark">{markOf(shown.judgement)}</span>
                    {labelOf(shown.judgement)}
                  </span>{' '}
                  <strong>{shown.san}</strong>. {shown.white ? 'White' : 'Black'} went from{' '}
                  {shown.win_before}% to {shown.win_after}%.
                  {shown.better_san && <> The engine wanted {shown.better_san}.</>}
                </p>
              ) : (
                <p className="analyse-said">The starting position. Pick a move to see it judged.</p>
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
                    <span className={`pip j-${m.judgement}`}>{markOf(m.judgement)}</span>
                    <span className="analyse-mark">{labelOf(m.judgement)}</span>
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
