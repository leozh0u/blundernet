// Hints are drawn on an SVG laid over the board rather than by tinting
// squares. A tinted square cannot point anywhere, and the whole idea of a
// hint ladder is that it points: first at the piece, then at where it goes.
//
// The viewBox is 8 by 8, one unit per square, so nothing here has to know the
// board's pixel size. It scales with whatever the board is rendered at.
export default function BoardOverlay({ orientation = 'white', glow, arrow }) {
  const centre = (square) => {
    const file = square.charCodeAt(0) - 97
    const rank = Number(square[1])
    return orientation === 'white'
      ? { x: file + 0.5, y: 8 - rank + 0.5 }
      : { x: 7 - file + 0.5, y: rank - 0.5 }
  }

  let line = null
  if (arrow) {
    const from = centre(arrow.from)
    const to = centre(arrow.to)
    // Stop the line short of the centre so the arrowhead sits on the square
    // rather than covering the piece standing there.
    const dx = to.x - from.x
    const dy = to.y - from.y
    const len = Math.hypot(dx, dy) || 1
    const back = 0.3
    line = {
      x1: from.x + (dx / len) * 0.32,
      y1: from.y + (dy / len) * 0.32,
      x2: to.x - (dx / len) * back,
      y2: to.y - (dy / len) * back,
    }
  }

  if (!glow && !line) return null

  return (
    <svg className="board-overlay" viewBox="0 0 8 8" aria-hidden="true">
      <defs>
        <marker
          id="hint-arrowhead"
          viewBox="0 0 6 6"
          refX="3"
          refY="3"
          markerWidth="3"
          markerHeight="3"
          orient="auto"
        >
          <path d="M0,0 L6,3 L0,6 z" fill="currentColor" />
        </marker>
      </defs>

      {glow && (
        <rect
          className="glow"
          x={centre(glow).x - 0.46}
          y={centre(glow).y - 0.46}
          width="0.92"
          height="0.92"
          rx="0.1"
        />
      )}

      {line && (
        <line
          className="arrow"
          x1={line.x1}
          y1={line.y1}
          x2={line.x2}
          y2={line.y2}
          markerEnd="url(#hint-arrowhead)"
        />
      )}
    </svg>
  )
}
