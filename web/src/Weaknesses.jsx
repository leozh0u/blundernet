import { useEffect, useState } from 'react'

// What you are worse at than the rest of your own play.
//
// The design job here is restraint. The temptation is to rank every theme and
// show a leaderboard of your failings, which looks impressive and is mostly
// noise: with forty puzzles behind you, most of that ordering is luck. So the
// server decides what can honestly be said, and this shows only that.

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
  doubleCheck: 'Double check',
}

const readable = (t) =>
  LABELS[t] || t.replace(/([A-Z])/g, ' $1').replace(/^./, (c) => c.toUpperCase())

const pct = (v) => `${Math.round(v * 100)}%`

export default function Weaknesses({ onDrill }) {
  const [data, setData] = useState(null)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    fetch('/api/me/weaknesses', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then(setData)
      .catch(() => {})
      .finally(() => setLoaded(true))
  }, [])

  if (!loaded || !data) return null

  const weak = data.themes.filter((t) => t.verdict === 'weak')
  const strong = data.themes.filter((t) => t.verdict === 'strong')

  return (
    <section className="weak">
      <h2>What to work on</h2>

      {data.attempts < 10 ? (
        <p className="weak-note">
          Solve a few more puzzles and this will start telling you what you are
          finding hard. It needs enough attempts to be sure rather than quick.
        </p>
      ) : weak.length === 0 && strong.length === 0 ? (
        <p className="weak-note">
          Nothing stands out yet across {data.attempts} puzzles. You are about
          as good at each of these as you are at the rest, which is a real
          answer and not a missing one.
        </p>
      ) : (
        <>
          {weak.length > 0 && (
            <ul className="weak-list">
              {weak.map((t) => (
                <li key={t.theme}>
                  <span className="weak-name">{readable(t.theme)}</span>
                  <span className="weak-rate">
                    {pct(t.rate)} of {t.attempts}
                  </span>
                  <span className="weak-against">against {pct(t.baseline)} elsewhere</span>
                  <button className="link" onClick={() => onDrill(t.theme)}>
                    practise
                  </button>
                </li>
              ))}
            </ul>
          )}

          {strong.length > 0 && (
            <p className="weak-note">
              Better than your average at{' '}
              {strong.map((t) => readable(t.theme).toLowerCase()).join(', ')}.
            </p>
          )}
        </>
      )}

      <p className="weak-fine">
        Counted on your first attempt at each puzzle, and only shown where there
        is enough evidence to be confident rather than merely to look busy.
      </p>
    </section>
  )
}
