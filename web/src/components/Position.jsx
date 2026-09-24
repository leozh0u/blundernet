// Reading a chess position without seeing it.
//
// A board drawn as coloured squares with piece images is unreadable to a
// screen reader: it is sixty-four divs and some pictures. Chess is unusually
// well suited to being fixed here, because the position already has a precise
// spoken form that players use out loud, so nothing has to be invented.
//
// Two things live here. A description of where everything stands, which a
// reader can be pointed at on demand, and an announcement of the move that was
// just played, which is what somebody following along actually needs.

const NAMES = {
  p: 'pawn',
  n: 'knight',
  b: 'bishop',
  r: 'rook',
  q: 'queen',
  k: 'king',
}

const FILES = 'abcdefgh'

// Squares are read out as "e4" rather than "e four", because that is how a
// player says it, and screen readers pronounce the pair correctly.
function listFor(board, colour) {
  const found = []
  for (const file of FILES) {
    for (let rank = 1; rank <= 8; rank++) {
      const square = file + rank
      const piece = board.get(square)
      if (piece && piece.color === colour) {
        found.push({ name: NAMES[piece.type], square })
      }
    }
  }
  // Kings and queens first, then down the value order, so the important
  // pieces are heard before a list of pawns.
  const order = ['king', 'queen', 'rook', 'bishop', 'knight', 'pawn']
  found.sort((a, b) => order.indexOf(a.name) - order.indexOf(b.name))
  return found.map((p) => `${p.name} ${p.square}`).join(', ')
}

export function describe(board) {
  if (!board) return ''
  const white = listFor(board, 'w')
  const black = listFor(board, 'b')
  const turn = board.turn() === 'w' ? 'White' : 'Black'
  let text = `${turn} to move. White: ${white || 'nothing'}. Black: ${black || 'nothing'}.`
  if (board.isCheckmate()) text += ' Checkmate.'
  else if (board.inCheck()) text += ' Check.'
  return text
}

// PositionText is the position as words, kept out of sight but in the
// accessibility tree. Not aria-live: a full board read out after every move
// would bury the thing that changed.
export function PositionText({ board, label = 'Position' }) {
  if (!board) return null
  return (
    <div className="visually-hidden" role="region" aria-label={label}>
      {describe(board)}
    </div>
  )
}

// Announce is the one line worth interrupting for. Polite rather than
// assertive, so it waits for the reader to finish its sentence instead of
// cutting across it.
export function Announce({ children }) {
  return (
    <div className="visually-hidden" role="status" aria-live="polite">
      {children}
    </div>
  )
}

// BlindfoldButton says what it will do, not what is on, which is the rule for
// a toggle whose effect is not visible in the control itself. aria-pressed
// carries the state for anyone who cannot see the board change.
export function BlindfoldButton({ on, onToggle, className = 'ghost wide' }) {
  return (
    <button type="button" className={className} onClick={onToggle} aria-pressed={on}>
      {on ? 'Show the pieces' : 'Blindfold'}
    </button>
  )
}
