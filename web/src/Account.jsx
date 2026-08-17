import { useCallback, useEffect, useState } from 'react'
import { auth } from './auth.js'

// The account strip. Everything on the site works signed out, so this never
// blocks anything: it names you, links to the page where the numbers are, and
// offers to make the account permanent.
//
// The rating used to live here as a bare number, which raised the question it
// could not answer once there were two of them. It moved to the account page,
// where each one is labelled with what moves it.
export default function Account({ refreshKey }) {
  const [user, setUser] = useState(null)
  const [profile, setProfile] = useState(null)
  const [loaded, setLoaded] = useState(false)
  const [form, setForm] = useState(null) // null, 'signup' or 'login'

  const load = useCallback(async () => {
    const [u, p] = await Promise.all([auth.me(), auth.profile()])
    setUser(u)
    setProfile(p)
    setLoaded(true)
  }, [])

  // refreshKey changes when a game ends, which is the only time the rating
  // moves.
  useEffect(() => {
    load()
  }, [load, refreshKey])

  // Ranked puzzles are the one thing on the site that needs an account, so
  // that page asks for the dialog rather than owning a second copy of it. An
  // event rather than lifted state: this is the only caller, and hoisting the
  // form into App to serve it would spread auth across three components.
  useEffect(() => {
    const open = (e) => setForm(e.detail === 'login' ? 'login' : 'signup')
    window.addEventListener('blundernet:auth', open)
    return () => window.removeEventListener('blundernet:auth', open)
  }, [])

  const isGuest = !user || user.guest

  // Reserve the space before the numbers arrive, so the strip does not jump
  // the page down once it loads.
  if (!loaded) {
    return (
      <div className="account">
        <span className="skeleton skel-name" aria-hidden="true" />
        <span className="visually-hidden">Loading your account</span>
      </div>
    )
  }

  return (
    <div className="account">
      {isGuest ? (
        <>
          <a className="whoami" href="/me">
            Guest
          </a>
          <button className="link" onClick={() => setForm('signup')}>
            Keep my progress
          </button>
          <button className="link quiet" onClick={() => setForm('login')}>
            Sign in
          </button>
        </>
      ) : (
        <>
          <a className="whoami" href="/me">
            {user.username}
          </a>
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
            // Ranked mode is gated on having an account, so it needs to know
            // the moment one exists rather than on the next page load.
            window.dispatchEvent(new CustomEvent('blundernet:authed'))
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
