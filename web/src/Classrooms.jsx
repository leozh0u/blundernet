import { useCallback, useEffect, useState } from 'react'
import { auth } from './auth.js'
import CoachBoard from './CoachBoard.jsx'

// Classrooms. A coach opens a room and reads out a code; the class joins with
// it and the coach sees what everyone is getting wrong.
//
// One component for both roles rather than a coach page and a student page,
// because they are the same page with different rows in it and the server
// already decides which rows you get. Two components would mean two places
// that believe something about who you are, and the browser's belief is the
// one that does not count.

const api = {
  async call(method, path, body) {
    const res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (res.status === 204) return null
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(data.error || 'Something went wrong.')
    return data
  },
  list: () => api.call('GET', '/api/classrooms'),
  create: (name) => api.call('POST', '/api/classrooms', { name }),
  join: (code) => api.call('POST', '/api/classrooms/join', { code }),
  room: (id) => api.call('GET', `/api/classrooms/${id}`),
  rotate: (id) => api.call('POST', `/api/classrooms/${id}/code`),
  remove: (id, user) => api.call('DELETE', `/api/classrooms/${id}/members/${user}`),
  close: (id) => api.call('DELETE', `/api/classrooms/${id}`),
}

// The code is stored and compared without the dash. It is shown with one
// because six characters read off a screen in one run is where people drop a
// character.
//
// The dash is its own element so it can be excluded from a selection. People
// copy a short code by dragging across it rather than hunting for a copy
// button, and a dash that comes along for the ride turns a working code into
// one the server has to be forgiving about. It is forgiving, but the paste
// should be right in the first place.
function Code({ code }) {
  if (!code) return null
  return (
    <>
      {code.slice(0, 3)}
      <span className="code-sep" aria-hidden="true">
        -
      </span>
      {code.slice(3)}
    </>
  )
}

// Same forgiveness the server applies, done in the field so the person typing
// sees their input tidied rather than silently corrected on submit.
const tidyCode = (raw) =>
  raw.toUpperCase().replace(/[^0-9A-HJKMNP-TV-Z]/g, '').slice(0, 6)

const since = (iso) => {
  if (!iso) return 'never'
  const days = Math.floor((Date.now() - new Date(iso)) / 86400000)
  if (days <= 0) return 'today'
  if (days === 1) return 'yesterday'
  if (days < 30) return `${days} days ago`
  return new Date(iso).toLocaleDateString()
}

export default function Classrooms({ roomID, onOpen }) {
  const [user, setUser] = useState(null)
  const [loaded, setLoaded] = useState(false)
  const [rooms, setRooms] = useState([])
  const [detail, setDetail] = useState(null)
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)

  const load = useCallback(async () => {
    const me = await auth.me()
    setUser(me)
    if (me && !me.guest) {
      try {
        setRooms((await api.list())?.classrooms || [])
      } catch {
        // A failed list is not worth an error banner over the whole page: the
        // create and join forms still work and will say what went wrong.
      }
    }
    setLoaded(true)
  }, [])

  useEffect(() => {
    load()
  }, [load])

  // The open room is fetched separately, because the list deliberately does
  // not carry rosters: a coach in six rooms should not pull six rosters to
  // look at one.
  useEffect(() => {
    if (!roomID) {
      setDetail(null)
      return
    }
    let live = true
    api
      .room(roomID)
      .then((d) => live && setDetail(d))
      .catch((e) => live && setError(e.message))
    return () => {
      live = false
    }
  }, [roomID])

  const run = async (fn) => {
    setError('')
    try {
      await fn()
    } catch (e) {
      setError(e.message)
    }
  }

  const create = (e) => {
    e.preventDefault()
    run(async () => {
      const room = await api.create(name)
      setName('')
      setRooms((r) => [room, ...r])
      onOpen(room.id)
    })
  }

  const join = (e) => {
    e.preventDefault()
    run(async () => {
      const room = await api.join(code)
      setCode('')
      setRooms((r) => (r.some((x) => x.id === room.id) ? r : [room, ...r]))
      onOpen(room.id)
    })
  }

  const rotate = () =>
    run(async () => {
      const { join_code } = await api.rotate(detail.classroom.id)
      setDetail((d) => ({ ...d, classroom: { ...d.classroom, join_code } }))
    })

  const remove = (userID) =>
    run(async () => {
      await api.remove(detail.classroom.id, userID)
      const fresh = await api.room(detail.classroom.id)
      setDetail(fresh)
    })

  // Takes the id rather than reading the open room, so the list can end a
  // session without opening it first. Sessions are kept on purpose, since a
  // team practising every Tuesday wants the same room and the same roster next
  // week, but a room started by mistake should not need two clicks to undo.
  const close = (id) =>
    run(async () => {
      await api.close(id)
      setRooms((r) => r.filter((x) => x.id !== id))
      if (detail?.classroom.id === id) onOpen(null)
    })

  const leaveRoom = (id) =>
    run(async () => {
      await api.remove(id, user.id)
      setRooms((r) => r.filter((x) => x.id !== id))
      if (detail?.classroom.id === id) onOpen(null)
    })

  const copy = async () => {
    await navigator.clipboard.writeText(detail.classroom.join_code)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  if (!loaded) return <section className="rooms" />

  // Guests are refused by the server, so saying so here is the honest version
  // rather than showing a form that will fail.
  if (!user || user.guest) {
    return (
      <section className="rooms">
        {/* The heading above already says what this is for, so this says
            only the thing it cannot: why this one page refuses a guest. */}
        <p className="rooms-note">
          Needs an account. Guest progress lives in this browser only, so a
          roster of guests would be empty by next week.
        </p>
      </section>
    )
  }

  if (detail) {
    const room = detail.classroom
    const coach = room.role === 'coach'
    return (
      <section className="rooms">
        <button className="link quiet back" onClick={() => onOpen(null)}>
          &lt; Back
        </button>
        <div className="room-head">
          <h2>{room.name}</h2>
          <span className="room-role">{coach ? 'Coach' : 'Student'}</span>
        </div>
        {error && <p className="rooms-error">{error}</p>}

        {coach && (
          <div className="room-code">
            <div>
              <span className="room-code-label">Join code</span>
              <strong>
                <Code code={room.join_code} />
              </strong>
            </div>
            <div className="room-code-actions">
              <button className="link" onClick={copy}>
                {copied ? 'copied' : 'copy'}
              </button>
              <button className="link quiet" onClick={rotate}>
                new code
              </button>
            </div>
          </div>
        )}

        <table className="roster">
          <thead>
            <tr>
              <th>Who</th>
              <th>Rating</th>
              <th>Solved</th>
              <th>Last seen</th>
              {coach && <th />}
            </tr>
          </thead>
          <tbody>
            {detail.members.map((m) => (
              <tr key={m.user_id}>
                <td>
                  {m.username}
                  {m.role === 'coach' && <span className="tag">coach</span>}
                </td>
                <td className="num">{m.rating}</td>
                <td className="num">
                  {m.attempts === 0 ? 'nothing yet' : `${m.solved} of ${m.attempts}`}
                </td>
                <td>{since(m.last_active)}</td>
                {coach && (
                  <td>
                    {m.user_id !== user.id && (
                      <button className="link quiet" onClick={() => remove(m.user_id)}>
                        remove
                      </button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {!coach && (
          <p className="rooms-note">Your coach can see your puzzle work. Nobody else can.</p>
        )}

        {coach && (
          <div className="room-board">
            <CoachBoard />
          </div>
        )}

        <div className="room-actions">
          {coach ? (
            <button className="link quiet" onClick={() => close(room.id)}>
              Close this session
            </button>
          ) : (
            <button className="link quiet" onClick={() => leaveRoom(room.id)}>
              Leave
            </button>
          )}
        </div>
      </section>
    )
  }

  return (
    <section className="rooms">
      {error && <p className="rooms-error">{error}</p>}
      {rooms.length > 0 && (
        <ul className="room-list">
          {rooms.map((r) => (
            <li key={r.id}>
              <button className="room-open" onClick={() => onOpen(r.id)}>
                <span className="room-name">{r.name}</span>
                <span className="room-meta">
                  {r.role === 'coach' ? 'coach' : 'student'} ·{' '}
                  {r.members === 1 ? '1 member' : `${r.members} members`}
                </span>
              </button>
              <button
                className="link quiet room-end"
                onClick={() => (r.role === 'coach' ? close(r.id) : leaveRoom(r.id))}
              >
                {r.role === 'coach' ? 'close' : 'leave'}
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="room-forms">
        <form onSubmit={join}>
          <label htmlFor="room-code">Join with a code</label>
          <div className="row">
            <input
              id="room-code"
              value={tidyCode(code)}
              onChange={(e) => setCode(tidyCode(e.target.value))}
              placeholder="ABC123"
              autoComplete="off"
            />
            <button disabled={tidyCode(code).length !== 6}>Join</button>
          </div>
        </form>

        <form onSubmit={create}>
          <label htmlFor="room-name">Start a session</label>
          <div className="row">
            <input
              id="room-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Tuesday squad"
              maxLength={60}
            />
            <button className="ghost" disabled={!name.trim()}>
              Create
            </button>
          </div>
        </form>
      </div>
    </section>
  )
}
