import { useEffect, useState } from 'react'
import { Chessboard, ChessboardDnDProvider, SparePiece } from 'react-chessboard'
import { useBoardWidth } from './board.js'
import BoardOverlay from './BoardOverlay.jsx'

// A board with the rules switched off, for a coach setting a position up in
// front of a class.
//
// Everywhere else on the site the board is driven by a chess.js game, which
// refuses an illegal move by design: that is the whole point when somebody is
// solving a puzzle. Here it is the wrong tool. A coach demonstrating a fork
// wants to put a knight on e5 because the lesson needs a knight on e5, not
// because a knight could legally get there, and they want to take both queens
// off and add a third rook if that is what makes the point.
//
// So the position is a plain map of square to piece rather than a chess.js
// instance. That is the entire design decision here. A map can hold a position
// no legal game could reach, which is exactly what a physical board with a box
// of spare pieces can hold.

const FILES = 'abcdefgh'
const WHITE = ['wK', 'wQ', 'wR', 'wB', 'wN', 'wP']
const BLACK = ['bK', 'bQ', 'bR', 'bB', 'bN', 'bP']

const START = {
  a8: 'bR', b8: 'bN', c8: 'bB', d8: 'bQ', e8: 'bK', f8: 'bB', g8: 'bN', h8: 'bR',
  a7: 'bP', b7: 'bP', c7: 'bP', d7: 'bP', e7: 'bP', f7: 'bP', g7: 'bP', h7: 'bP',
  a2: 'wP', b2: 'wP', c2: 'wP', d2: 'wP', e2: 'wP', f2: 'wP', g2: 'wP', h2: 'wP',
  a1: 'wR', b1: 'wN', c1: 'wB', d1: 'wQ', e1: 'wK', f1: 'wB', g1: 'wN', h1: 'wR',
}

// Castling and en passant are written as unavailable rather than guessed at.
// A position somebody arranged by hand has no history, so there is no honest
// answer to "may this king still castle", and inventing one would put a claim
// in the FEN that the board never made.
function toFEN(position, turn) {
  const rows = []
  for (let rank = 8; rank >= 1; rank--) {
    let row = ''
    let gap = 0
    for (const file of FILES) {
      const piece = position[file + rank]
      if (!piece) {
        gap += 1
        continue
      }
      if (gap) {
        row += gap
        gap = 0
      }
      const letter = piece[1]
      row += piece[0] === 'w' ? letter.toUpperCase() : letter.toLowerCase()
    }
    if (gap) row += gap
    rows.push(row)
  }
  return `${rows.join('/')} ${turn} - - 0 1`
}

// The placement and whose move it is. The rest of a pasted FEN, castling
// rights and the clocks, describes a game this board is not playing.
function fromFEN(fen) {
  const fields = fen.trim().split(/\s+/)
  const placement = fields[0]
  const ranks = placement.split('/')
  if (ranks.length !== 8) return null
  const position = {}
  for (let i = 0; i < 8; i++) {
    const rank = 8 - i
    let file = 0
    for (const ch of ranks[i]) {
      if (ch >= '1' && ch <= '8') {
        file += Number(ch)
        continue
      }
      if (!'KQRBNPkqrbnp'.includes(ch)) return null
      if (file > 7) return null
      position[FILES[file] + rank] = (ch === ch.toUpperCase() ? 'w' : 'b') + ch.toUpperCase()
      file += 1
    }
    if (file !== 8) return null
  }
  return { position, turn: fields[1] === 'b' ? 'b' : 'w' }
}

export default function CoachBoard() {
  const [frame, width] = useBoardWidth()
  const [position, setPosition] = useState(START)
  const [turn, setTurn] = useState('w')
  const [orientation, setOrientation] = useState('white')
  const [fen, setFen] = useState('')
  const [openings, setOpenings] = useState([])
  const [loadingOpening, setLoadingOpening] = useState(false)
  const [copied, setCopied] = useState(false)
  const [bad, setBad] = useState(false)
  const [selected, setSelected] = useState(null)

  const current = toFEN(position, turn)

  useEffect(() => {
    fetch('/api/puzzles/openings')
      .then((r) => (r.ok ? r.json() : { openings: [] }))
      .then((body) => setOpenings(body.openings || []))
      .catch(() => {})
  }, [])

  // Pulling a real position out of the corpus rather than shipping an opening
  // book. The puzzles are tagged with the opening they came from, so asking
  // for one is asking the search that already exists a narrower question, and
  // what a coach gets is a position somebody actually reached rather than a
  // textbook line ending on move six.
  const loadOpening = async (name) => {
    if (!name) return
    setLoadingOpening(true)
    setBad(false)
    try {
      const res = await fetch(`/api/puzzles?opening=${encodeURIComponent(name)}&limit=1`)
      const body = await res.json()
      const puzzle = body.puzzles?.[0]
      if (!puzzle) {
        setBad(true)
        return
      }
      const parsed = fromFEN(puzzle.fen)
      if (parsed) {
        setPosition(parsed.position)
        setTurn(parsed.turn)
      }
    } catch {
      setBad(true)
    } finally {
      setLoadingOpening(false)
    }
  }

  // Every drop returns true, because there is nothing here that can be
  // refused. A piece landing on an occupied square replaces what was there,
  // the way it does when you put a piece down on a real board.
  const movePiece = (from, to) => {
    setPosition((p) => {
      const next = { ...p }
      const piece = next[from]
      delete next[from]
      if (piece) next[to] = piece
      return next
    })
    return true
  }

  const addPiece = (piece, square) => {
    setPosition((p) => ({ ...p, [square]: piece }))
    return true
  }

  // Clicking works as well as dragging, because dragging is the thing a coach
  // cannot do while talking and pointing at a screen, and on a touchscreen it
  // is the thing that fights scrolling. Click the piece, click where it goes.
  // Clicking a piece already selected puts it back down.
  const clickSquare = (square) => {
    if (selected === square) {
      setSelected(null)
      return
    }
    if (selected) {
      movePiece(selected, square)
      setSelected(null)
      return
    }
    if (position[square]) setSelected(square)
  }

  const removePiece = (square) => {
    setPosition((p) => {
      const next = { ...p }
      delete next[square]
      return next
    })
  }

  const load = () => {
    const parsed = fromFEN(fen)
    if (!parsed) {
      setBad(true)
      return
    }
    setBad(false)
    setPosition(parsed.position)
    setTurn(parsed.turn)
    setFen('')
  }

  const copy = async () => {
    await navigator.clipboard.writeText(current)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <ChessboardDnDProvider>
      <div className="coachboard">
        <div className="coachboard-main">
          {/* The trays sit above and below the board, on the side each
              colour starts on, so reaching for a white rook means reaching
              towards where white lives. */}
          <div className="tray">
            {width > 0 &&
              (orientation === 'white' ? BLACK : WHITE).map((piece) => (
                <SparePiece key={piece} piece={piece} width={width / 8} dndId="CoachBoard" />
              ))}
          </div>

          <div className="coachboard-frame" ref={frame}>
            {width > 0 && (
            <Chessboard
              id="CoachBoard"
              boardWidth={width}
              position={position}
              boardOrientation={orientation}
              onPieceDrop={(from, to) => {
                setSelected(null)
                return movePiece(from, to)
              }}
              onSquareClick={clickSquare}
              onSparePieceDrop={(piece, square) => addPiece(piece, square)}
              onPieceDropOffBoard={(square) => removePiece(square)}
              dropOffBoardAction="trash"
              animationDuration={0}
              customBoardStyle={{ borderRadius: '6px' }}
              customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
              customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
            />
            )}
            {/* The selected square is drawn on the overlay the hints already
                use, rather than through the board's own square styling, which
                this board does not pick up. One way of marking a square on the
                site, and it scales with the board because the overlay is an
                8 by 8 viewBox. */}
            <BoardOverlay orientation={orientation} glow={selected} />
          </div>

          <div className="tray">
            {width > 0 &&
              (orientation === 'white' ? WHITE : BLACK).map((piece) => (
                <SparePiece key={piece} piece={piece} width={width / 8} dndId="CoachBoard" />
              ))}
          </div>
        </div>

        <aside className="coachboard-side">
          <h3>Set up a position</h3>
          <p className="coachboard-help">
            Drag anything anywhere. Off the board removes it. The trays never
            run out.
          </p>

          <div className="coachboard-actions">
            <button className="ghost" onClick={() => setPosition(START)}>
              Start position
            </button>
            <button className="ghost" onClick={() => setPosition({})}>
              Empty board
            </button>
            <button
              className="ghost"
              onClick={() => setOrientation((o) => (o === 'white' ? 'black' : 'white'))}
            >
              Flip
            </button>
          </div>

          <div className="coachboard-turn">
            <span>To move</span>
            <div>
              <button
                className={`chip ${turn === 'w' ? 'on' : ''}`}
                onClick={() => setTurn('w')}
              >
                White
              </button>
              <button
                className={`chip ${turn === 'b' ? 'on' : ''}`}
                onClick={() => setTurn('b')}
              >
                Black
              </button>
            </div>
          </div>

          <label className="coachboard-fen-label" htmlFor="coach-fen">
            Position code <span className="coachboard-aka">FEN</span>
          </label>
          <output className="coachboard-fen" id="coach-fen">
            {current}
          </output>
          <button className="link" onClick={copy}>
            {copied ? 'copied' : 'copy'}
          </button>

          <div className="coachboard-load">
            <input
              value={fen}
              onChange={(e) => {
                setFen(e.target.value)
                setBad(false)
              }}
              placeholder="Paste a position code"
              aria-label="Paste a position code"
            />
            <button className="ghost" onClick={load} disabled={!fen.trim()}>
              Load
            </button>
          </div>
          {bad && <p className="coachboard-bad">That is not a position I can read.</p>}

          <label className="coachboard-fen-label opening-label" htmlFor="coach-opening">
            Start from an opening
          </label>
          <select
            id="coach-opening"
            className="coachboard-opening"
            defaultValue=""
            disabled={loadingOpening}
            onChange={(e) => loadOpening(e.target.value)}
          >
            <option value="">Pick one</option>
            {openings.map((o) => (
              <option key={o.name} value={o.name}>
                {o.name.replace(/_/g, ' ')}
              </option>
            ))}
          </select>
        </aside>
      </div>
    </ChessboardDnDProvider>
  )
}
