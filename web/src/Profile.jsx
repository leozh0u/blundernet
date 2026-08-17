import { useCallback, useEffect, useState } from 'react'

// The account page. It exists because the site now keeps four numbers about
// you, and a bare "1500" in the corner of the header says none of them: two
// separate ratings, a bot level, and the lists you have built up. Every number
// here is labelled and says what moves it.

const fmt = (n) => (n === null || n === undefined ? '...' : Math.round(n))

export default function Profile({ onDrill }) {
  const [profile, setProfile] = useState(null)
  const [games, setGames] = useState([])
  const [loaded, setLoaded] = useState(false)

  const load = useCallback(async () => {
    const [p, g] = await Promise.all([
      fetch('/api/me/profile').then((r) => (r.ok ? r.json() : null)),
      fetch('/api/me/games?limit=8').then((r) => (r.ok ? r.json() : [])),
    ])
    setProfile(p)
    setGames(Array.isArray(g) ? g : g?.games || [])
    setLoaded(true)
  }, [])

  useEffect(() => {
    load()
  }, [load])

  if (!loaded) {
    return (
      <div className="pagehead">
        <h1>Account</h1>
      </div>
    )
  }

  const guest = !profile || profile.guest

  return (
    <section className="profile">
      <div className="pagehead">
        <h1>{guest ? 'Guest' : profile.username}</h1>
      </div>

      {guest && (
        <p className="note">
          You are playing as a guest. Everything below is already yours and
          signing up keeps it on a name you can log back into.
        </p>
      )}

      <div className="cards">
        <div className="card">
          <h2>Playing</h2>
          <dl className="meta">
            <div>
              <dt>Rating</dt>
              <dd>
                {fmt(profile?.rating)}
                {profile?.provisional && <i className="prov">?</i>}
              </dd>
            </div>
            <div>
              <dt>Rated games</dt>
              <dd>{profile?.rated_games ?? 0}</dd>
            </div>
            <div>
              <dt>Bot level</dt>
              <dd>{profile?.bot_level ?? 3}</dd>
            </div>
          </dl>
          <p className="explains">
            Moves when you finish a rated game against the bot. The level goes
            up a rung when you win and down when you lose. A question mark
            means fewer than five rated games, so the number is still settling.
          </p>
        </div>

        <div className="card">
          <h2>Puzzles</h2>
          <dl className="meta">
            <div>
              <dt>Rating</dt>
              <dd>{fmt(profile?.puzzle_rating)}</dd>
            </div>
            <div>
              <dt>Solved</dt>
              <dd>{profile?.puzzles_solved ?? 0}</dd>
            </div>
            <div>
              <dt>Seen</dt>
              <dd>{profile?.puzzles_tried ?? 0}</dd>
            </div>
            <div>
              <dt>Best streak</dt>
              <dd>{profile?.best_streak ?? 0}</dd>
            </div>
          </dl>
          <p className="explains">
            A separate rating, because tactics and playing are different
            skills. Only ranked puzzles move it. Learning mode never does.
          </p>
        </div>

        <div className="card">
          <h2>Your lists</h2>
          <dl className="meta">
            <div>
              <dt>Saved</dt>
              <dd>{profile?.favourites ?? 0}</dd>
            </div>
            <div>
              <dt>To review</dt>
              <dd>{profile?.to_review ?? 0}</dd>
            </div>
          </dl>
          <div className="after">
            <button className="wide" onClick={() => onDrill('saved')}>
              Drill saved
            </button>
            <button className="ghost wide" onClick={() => onDrill('wrong')}>
              Drill the ones I got wrong
            </button>
          </div>
        </div>
      </div>

      <div className="card">
        <h2>Recent games</h2>
        {games.length === 0 ? (
          <p className="explains">Nothing finished yet.</p>
        ) : (
          <table className="history">
            <tbody>
              {games.map((g) => (
                <tr key={g.id}>
                  <td>{g.player_color === 'white' ? 'White' : 'Black'}</td>
                  <td>{outcome(g)}</td>
                  <td>{g.termination}</td>
                  <td className="num">{g.ply} plies</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  )
}

function outcome(g) {
  if (g.result === '1/2-1/2') return 'Draw'
  const won =
    (g.result === '1-0' && g.player_color === 'white') ||
    (g.result === '0-1' && g.player_color === 'black')
  return won ? 'Won' : 'Lost'
}
