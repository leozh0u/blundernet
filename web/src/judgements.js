// What each verdict is called, and what it looks like.
//
// One place, because the review page, the post-game panel and the board all
// have to agree. A move labelled "Blunder" in the list and tinted the same
// colour as "Good" on the board is worse than no colour at all.
//
// The marks are the ones chess notation already uses, so nobody has to be
// taught them: !! for brilliant, ?? for a blunder. Best gets a star rather
// than a symbol, because "!" already means great and doubling it up would
// blur the two labels the review works hardest to separate.
export const CLASS = {
  brilliant: { label: 'Brilliant', mark: '!!' },
  great: { label: 'Great', mark: '!' },
  best: { label: 'Best', mark: '★' },
  excellent: { label: 'Excellent', mark: '✓' },
  good: { label: 'Good', mark: '✓' },
  inaccuracy: { label: 'Inaccuracy', mark: '?!' },
  mistake: { label: 'Mistake', mark: '?' },
  blunder: { label: 'Blunder', mark: '??' },
}

// The order they are summarised in: best news first, worst last. Also the
// order the counts row reads in, which is the order a player scans.
export const ORDER = [
  'brilliant',
  'great',
  'best',
  'excellent',
  'good',
  'inaccuracy',
  'mistake',
  'blunder',
]

export const labelOf = (j) => CLASS[j]?.label || j
export const markOf = (j) => CLASS[j]?.mark || ''

// Whether a verdict is worth drawing attention to on the board. Six of the
// eight are ordinary moves, and putting a badge on every one of them turns the
// board into a rash and tells you nothing.
export const notable = (j) =>
  j === 'brilliant' || j === 'great' || j === 'inaccuracy' || j === 'mistake' || j === 'blunder'
