import { useEffect, useState } from 'react'
import { auth } from './auth.js'
import RecoveryCode from './RecoveryCode.jsx'

// The recovery code panel on the account page.
//
// Two people need this. Anyone who signed up before recovery codes existed has
// no code at all, and their account has no way back if they forget the
// password. Anyone who closed the signup panel without saving theirs is in the
// same position but does not know it, because the server holds a hash either
// way and cannot tell them what it was.
//
// So the page says which of the two states the account is in, and offers the
// same button for both. Generating requires the current password: a session
// left open on a shared computer should not be able to mint a permanent way
// back into the account.
export default function RecoverySection({ user }) {
  const [has, setHas] = useState(Boolean(user?.has_recovery))
  const [password, setPassword] = useState('')
  const [issued, setIssued] = useState(null)
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)
  const [asking, setAsking] = useState(false)

  useEffect(() => setHas(Boolean(user?.has_recovery)), [user])

  async function generate(e) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const out = await auth.newRecoveryCode(password)
      setIssued(out.recovery_code)
      setHas(true)
      setAsking(false)
      setPassword('')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  if (issued) {
    return (
      <div className="card">
        <RecoveryCode
          code={issued}
          heading="Your new recovery code"
          blurb="This replaces any code you had before. It is shown once and the server keeps only a hash of it, so save it now."
          doneLabel="Done"
          onDone={() => setIssued(null)}
        />
      </div>
    )
  }

  return (
    <div className="card">
      <h2>Getting back in</h2>
      <p className="explains">
        {has
          ? 'You have a recovery code. If you have lost it, generate a new one and the old one stops working.'
          : 'This account has no recovery code, so there is no way back into it if you forget the password. The site stores no email, so nobody can send you a reset link.'}
      </p>

      {asking ? (
        <form onSubmit={generate} className="recovery-gen">
          <label>
            Current password
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              autoFocus
              required
            />
          </label>
          {error && <p className="form-error">{error}</p>}
          <div className="modal-actions">
            <button type="button" className="link quiet" onClick={() => setAsking(false)}>
              Cancel
            </button>
            <button type="submit" disabled={busy}>
              {busy ? 'Working...' : 'Generate a code'}
            </button>
          </div>
        </form>
      ) : (
        <button className="wide" onClick={() => setAsking(true)}>
          {has ? 'Generate a new code' : 'Generate a recovery code'}
        </button>
      )}
    </div>
  )
}
