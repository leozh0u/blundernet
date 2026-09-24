import { useCallback, useMemo, useRef, useState } from 'react'

// Two things every board on this site needs and the board library gets wrong.

// How long one move takes to slide, matching react-chessboard's own default.
export const MOVE_MS = 300

// useBoardMotion decides whether the next position the board is handed should
// slide or appear.
//
// The library animates between any two positions it is given and cannot tell
// the difference between a move and a jump. A knight going to f3 should slide.
// The same board being replaced by a different puzzle, or rewound five plies
// by "play the answer", should not: animating that drags every piece across
// the board at once, which reads as the board glitching.
//
// It lives here because three modes need the same rule, and a rule pasted into
// three files is a rule that will be fixed in one of them.
export function useBoardMotion() {
  const [ms, setMs] = useState(0)
  const last = useRef(null)

  // The board is about to show a position `cursor` plies into the line it is
  // already on. Exactly one ply on from the last one is a move.
  //
  // Being asked twice for the same position has to be harmless. React runs
  // effects more than once for the same state, in development deliberately,
  // and an earlier version of this recomputed on the second call: the cursor
  // was no longer one ahead of itself, so it decided the move was a jump and
  // cancelled the animation it had just asked for.
  const step = useCallback((cursor) => {
    if (cursor === last.current) return
    setMs(last.current !== null && cursor === last.current + 1 ? MOVE_MS : 0)
    last.current = cursor
  }, [])

  // One move played onto the position already on screen.
  const move = useCallback(() => {
    setMs(MOVE_MS)
    last.current = null
  }, [])

  // A different puzzle, a different game. Nothing on screen relates to what
  // replaces it, so there is no movement to draw.
  //
  // `at` is where the board lands, for the caller that snaps to a position and
  // then plays a move from it. A puzzle appears and then its opening blunder
  // is played: the arrival is a jump, the blunder is a move, and without the
  // landing point the blunder would snap too.
  const jump = useCallback((at = null) => {
    setMs(0)
    last.current = at
  }, [])

  // Memoised, because callers put this in effect dependencies. A fresh object
  // every render would make any such effect run every render, and an effect
  // that sets state on every render is an infinite loop.
  return useMemo(() => ({ ms, step, move, jump }), [ms, step, move, jump])
}

// useBoardWidth measures the element the board is rendered into.
//
// Left to itself, react-chessboard only ever learns its width from inside a
// ResizeObserver callback, and it only attaches that observer if the element
// already had a width when the effect first ran. Two ways that goes wrong: the
// element is zero wide at that instant, so no observer is attached and the
// board renders as nothing forever; or the observer never fires, which is real
// in some embedded browsers, with the same result.
//
// So this measures the element directly the moment it attaches, and treats the
// observer as the update path rather than the only path.
export function useBoardWidth() {
  const [width, setWidth] = useState(0)
  const observer = useRef(null)

  // A callback ref rather than a ref plus an effect, because the element the
  // board goes into does not always exist when the component first renders:
  // the classroom question panel has no board until a question arrives. An
  // effect with an empty dependency list runs once, sees no element, and never
  // measures. A callback ref runs when the node attaches, whenever that is.
  const ref = useCallback((el) => {
    observer.current?.disconnect()
    observer.current = null
    if (!el) return

    const measure = () => setWidth(Math.floor(el.getBoundingClientRect().width))
    measure()

    // Only for what happens after: a window resize, the panel beside it
    // growing, the sidebar collapsing.
    if (typeof ResizeObserver === 'undefined') return
    observer.current = new ResizeObserver(measure)
    observer.current.observe(el)
  }, [])

  return [ref, width]
}

// useBlindfold is the pieces being hidden while everything else keeps working.
//
// Shared rather than repeated, because it exists in four modes and a rule
// pasted four times gets fixed in one of them. The state is a boolean; what
// makes it work is that the class hides the pieces with visibility rather than
// removing them, so they keep their squares, stay draggable, and stay in the
// accessibility tree for anyone reading the board as text.
export function useBlindfold() {
  const [on, setOn] = useState(false)
  const toggle = useCallback(() => setOn((v) => !v), [])
  return { on, toggle, className: on ? 'blindfold' : '' }
}
