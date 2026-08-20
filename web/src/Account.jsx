import { useCallback, useEffect, useState } from 'react'
import { auth, tidyRecoveryCode } from './auth.js'
import RecoveryCode from './RecoveryCode.jsx'

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
          {/* Says what it does. "Keep my progress" describes the benefit but
              leaves somebody guessing whether it signs them up, and the ranked
              gate two clicks away already calls it creating an account. */}
          <button className="link" onClick={() => setForm('signup')}>
            Create an account
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
  const [code, setCode] = useState('')
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)
  // The code handed back by signup or by a successful recovery. While this is
  // set the dialog shows nothing else: it is the only moment the plaintext
  // exists, and closing over it loses the account.
  const [issued, setIssued] = useState(null)
  const signingUp = mode === 'signup'
  const recovering = mode === 'recover'

  async function submit(e) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (recovering) {
        const out = await auth.recover(username, code, password)
        setIssued({
          code: out.recovery_code,
          heading: 'You are back in, and here is a new code',
          blurb:
            'Using a recovery code retires it, so this one replaces it. Everything else signed in as you has been signed out.',
          doneLabel: 'Done',
        })
      } else if (signingUp) {
        const out = await auth.signup(username, password)
        if (!out.recovery_code) {
          // The account exists; only the code failed. Better to continue than
          // to strand somebody on a dialog about a code that is not coming.
          onDone()
          return
        }
        setIssued({
          code: out.recovery_code,
          heading: 'Save your recovery code',
          blurb:
            'This is the only way back into your account if you forget your password. There is no email on this site, so nobody can send you a reset link. You will not see this code again.',
          doneLabel: 'Start playing',
        })
      } else {
        await auth.login(username, password)
        onDone()
      }
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  if (issued) {
    return (
      <div className="modal-back">
        <div className="modal" onClick={(e) => e.stopPropagation()}>
          <RecoveryCode
            code={issued.code}
            heading={issued.heading}
            blurb={issued.blurb}
            doneLabel={issued.doneLabel}
            onDone={onDone}
          />
        </div>
      </div>
    )
  }

  const title = recovering ? 'Use a recovery code' : signingUp ? 'Create an account' : 'Sign in'

  return (
    <div className="modal-back" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <h2>{title}</h2>

        {signingUp && hasProgress && (
          <p className="note">
            Your rating and games so far come with you. Nothing is lost.
          </p>
        )}
        {recovering && (
          <p className="note">
            Enter the code you were given when you signed up, and pick a new password.
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

        {recovering && (
          <label>
            Recovery code
            {/* Tidied as it is typed, so a code pasted in lower case or
                without its dashes visibly becomes the thing it is being
                compared against. The server normalises too; this is so the
                user can see their own input is being accepted. */}
            <input
              value={code}
              onChange={(e) => setCode(tidyRecoveryCode(e.target.value))}
              placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX"
              autoComplete="off"
              autoCapitalize="characters"
              spellCheck="false"
              className="code-input"
              required
            />
          </label>
        )}

        <label>
          {recovering ? 'New password' : 'Password'}
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={signingUp || recovering ? 'new-password' : 'current-password'}
            required
          />
        </label>

        {error && <p className="form-error">{error}</p>}

        <div className="modal-actions">
          <button type="button" className="link quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={busy}>
            {busy ? 'Working...' : recovering ? 'Set a new password' : signingUp ? 'Create account' : 'Sign in'}
          </button>
        </div>

        <p className="swap">
          {recovering ? (
            <>
              Remembered it?{' '}
              <button type="button" className="link" onClick={() => onSwitch('login')}>
                Sign in
              </button>
            </>
          ) : signingUp ? (
            <>
              Already have an account?{' '}
              <button type="button" className="link" onClick={() => onSwitch('login')}>
                Sign in
              </button>
            </>
          ) : (
            <>
              No account yet?{' '}
              <button type="button" className="link" onClick={() => onSwitch('signup')}>
                Create one
              </button>
              {' or '}
              <button type="button" className="link" onClick={() => onSwitch('recover')}>
                use a recovery code
              </button>
            </>
          )}
        </p>
      </form>
    </div>
  )
}
