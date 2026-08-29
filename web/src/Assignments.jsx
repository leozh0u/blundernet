import { useCallback, useEffect, useState } from 'react'

// Homework. A coach sets a target, a student sees how far along they are and
// clicks through to the puzzles that count.
//
// The "practise this" link is a plain URL into the drill, because the puzzle
// page already keeps its whole filter in the query string. That is the reason
// this feature is small: the thing a coach is setting is a filter, and filters
// were already links.

const THEMES = [
  'fork', 'pin', 'skewer', 'discoveredAttack', 'hangingPiece', 'backRankMate',
  'sacrifice', 'deflection', 'mateIn1', 'mateIn2', 'mateIn3', 'endgame',
]

const BANDS = [
  { label: 'Any rating', min: 0, max: 0 },
  { label: 'Under 1200', min: 0, max: 1200 },
  { label: '1200 to 1500', min: 1200, max: 1500 },
  { label: '1500 to 1800', min: 1500, max: 1800 },
  { label: 'Over 1800', min: 1800, max: 0 },
]

const readable = (t) =>
  t ? t.replace(/([A-Z])/g, ' $1').replace(/^./, (c) => c.toUpperCase()) : 'Any theme'

// The same query string the puzzle page parses, so the link lands on exactly
// the puzzles the assignment counts.
function drillURL(a) {
  const q = new URLSearchParams()
  if (a.theme) q.set('theme', a.theme)
  if (a.min_rating) q.set('rating_min', a.min_rating)
  if (a.max_rating) q.set('rating_max', a.max_rating)
  return `/?${q}`
}

export default function Assignments({ classroomID, role }) {
  const coach = role === 'coach'
  const [list, setList] = useState([])
  const [theme, setTheme] = useState('fork')
  const [band, setBand] = useState(0)
  const [target, setTarget] = useState(10)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/classrooms/${classroomID}/assignments`, {
        credentials: 'same-origin',
      })
      if (!res.ok) return
      setList((await res.json()).assignments || [])
    } catch {
      // Leaving the last good list on screen beats blanking it.
    }
  }, [classroomID])

  useEffect(() => {
    load()
  }, [load])

  const set = async (e) => {
    e.preventDefault()
    setError('')
    const b = BANDS[band]
    const res = await fetch(`/api/classrooms/${classroomID}/assignments`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        theme,
        min_rating: b.min,
        max_rating: b.max,
        target: Number(target),
      }),
    })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      setError(data.error || 'That could not be set.')
      return
    }
    load()
  }

  const drop = async (id) => {
    await fetch(`/api/classrooms/${classroomID}/assignments/${id}`, {
      method: 'DELETE',
      credentials: 'same-origin',
    })
    load()
  }

  return (
    <div className="work">
      <h3>Homework</h3>
      {error && <p className="rooms-error">{error}</p>}

      {list.length === 0 ? (
        <p className="coachboard-help">
          {coach ? 'Nothing set yet.' : 'Your coach has not set anything yet.'}
        </p>
      ) : (
        <ul className="work-list">
          {list.map((a) => {
            const done = Math.min(a.done, a.target)
            return (
              <li key={a.id}>
                <div className="work-what">
                  <span className="work-theme">{readable(a.theme)}</span>
                  <span className="work-band">
                    {a.min_rating || a.max_rating
                      ? `${a.min_rating || 'any'} to ${a.max_rating || 'any'}`
                      : 'any rating'}
                  </span>
                </div>

                {/* A bar rather than a number alone, because "3 of 10" is a
                    fact and a bar is a feeling, and homework needs both. */}
                <div
                  className="work-bar"
                  role="progressbar"
                  aria-valuenow={done}
                  aria-valuemin={0}
                  aria-valuemax={a.target}
                  aria-label={`${readable(a.theme)}, ${done} of ${a.target} done`}
                >
                  <span style={{ width: `${(done / a.target) * 100}%` }} />
                </div>

                <span className="work-count">
                  {coach ? `${a.class} finished` : `${done} of ${a.target}`}
                </span>

                {coach ? (
                  <button className="link quiet" onClick={() => drop(a.id)}>
                    remove
                  </button>
                ) : (
                  <a className="link" href={drillURL(a)}>
                    practise
                  </a>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {coach && (
        <form className="work-set" onSubmit={set}>
          <select value={theme} onChange={(e) => setTheme(e.target.value)} aria-label="Theme">
            <option value="">Any theme</option>
            {THEMES.map((t) => (
              <option key={t} value={t}>
                {readable(t)}
              </option>
            ))}
          </select>
          <select
            value={band}
            onChange={(e) => setBand(Number(e.target.value))}
            aria-label="Rating"
          >
            {BANDS.map((b, i) => (
              <option key={b.label} value={i}>
                {b.label}
              </option>
            ))}
          </select>
          <input
            type="number"
            min="1"
            max="100"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            aria-label="How many to solve"
          />
          <button className="ghost">Set</button>
        </form>
      )}
    </div>
  )
}
