import { useCallback, useEffect, useState } from 'react'
import { auth, displayRating } from './auth.js'

// The account strip. Everything on the site works signed out, so this never
// blocks anything: it reports the rating you already have and offers to make
// it permanent.
export default function Account({ refreshKey }) {
  const [user, setUser] = useState(null)
  const [profile, setProfile] = useState(null)
  const [form, setForm] = useState(null) // null, 'signup' or 'login'

  const load = useCallback(async () => {
    const [u, p] = await Promise.all([auth.me(), auth.profile()])
    setUser(u)
    setProfile(p)
  }, [])

  // refreshKey changes when a game ends, which is the only time the rating
  // moves.
  useEffect(() => {
    load()
  }, [load, refreshKey])

  const rating = displayRating(profile)
  const isGuest = !user || user.guest

  return (
    <div className="account">
      {rating !== null && (
        <span className="rating" title={profile.provisional ? 'Provisional until five rated games' : ''}>
          {rating}
          {profile.provisional && <i className="prov">?</i>}
        </span>
      )}

      {isGuest ? (
        <>
          <span className="whoami">Playing as guest</span>
          <button className="link" onClick={() => setForm('signup')}>
            Keep my progress
          </button>
          <button className="link quiet" onClick={() => setForm('login')}>
            Sign in
          </button>
        </>
      ) : (
        <>
          <span className="whoami">{user.username}</span>
          <button
            className="link quiet"
            onClick={async () => {
              await auth.logout()
              load()
            }}
          >
            Sign out
          </button>
        </>
      )}

      {form && (
        <AuthDialog
          mode={form}
          hasProgress={isGuest && profile && profile.rated_games > 0}
          onClose={() => setForm(null)}
          onDone={() => {
            setForm(null)
            load()
          }}
          onSwitch={(m) => setForm(m)}
        />
      )}
    </div>
  )
}

function AuthDialog({ mode, hasProgress, onClose, onDone, onSwitch }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)
  const signingUp = mode === 'signup'

  async function submit(e) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await (signingUp ? auth.signup(username, password) : auth.login(username, password))
      onDone()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-back" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <h2>{signingUp ? 'Create an account' : 'Sign in'}</h2>

        {signingUp && hasProgress && (
          <p className="note">
            Your rating and games so far come with you. Nothing is lost.
          </p>
        )}

        <label>
          Username
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </label>
        <label>
          Password
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={signingUp ? 'new-password' : 'current-password'}
            required
          />
        </label>

        {error && <p className="form-error">{error}</p>}

        <div className="modal-actions">
          <button type="button" className="link quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={busy}>
            {busy ? 'Working...' : signingUp ? 'Create account' : 'Sign in'}
          </button>
        </div>

        <p className="swap">
          {signingUp ? 'Already have an account? ' : 'No account yet? '}
          <button type="button" className="link" onClick={() => onSwitch(signingUp ? 'login' : 'signup')}>
            {signingUp ? 'Sign in' : 'Create one'}
          </button>
        </p>
      </form>
    </div>
  )
}
