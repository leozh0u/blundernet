import { useState } from 'react'

// "Something is broken" in the footer.
//
// Open to anybody, signed in or not, because the person most likely to hit a
// bug is the one who just arrived and will not make an account to tell you the
// board did not load. No name field and no email field: asking for either is a
// reason not to bother, and the page they were on is worth more than either.
export default function Feedback() {
  const [open, setOpen] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [error, setError] = useState(null)

  async function submit(e) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/feedback', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message, page: window.location.pathname }),
      })
      if (!res.ok) {
        const d = await res.json().catch(() => ({}))
        throw new Error(d.error || 'That did not send. Try again in a moment.')
      }
      setSent(true)
      setMessage('')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  function close() {
    setOpen(false)
    // Reset on close rather than on open, so the thank-you stays visible for
    // as long as the panel does.
    setSent(false)
    setError(null)
  }

  if (!open) {
    return (
      <button type="button" className="link quiet" onClick={() => setOpen(true)}>
        Something broken?
      </button>
    )
  }

  return (
    <div className="modal-back" onClick={close}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        {sent ? (
          <>
            <h2>Thank you</h2>
            <p className="note">
              That is stored and I will read it. There is nowhere to reply to,
              so if it needs a conversation, the repository issues are the place.
            </p>
            <div className="modal-actions">
              <button type="button" onClick={close}>
                Close
              </button>
            </div>
          </>
        ) : (
          <form onSubmit={submit}>
            <h2>Tell me what is wrong</h2>
            <p className="note">
              Bugs, puzzles that look incorrect, anything confusing, anything
              you wish it did. The page you are on is sent along with it.
            </p>
            <label>
              <span className="visually-hidden">Your message</span>
              <textarea
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                rows={5}
                maxLength={2000}
                placeholder="The board did not load on..."
                autoFocus
                required
              />
            </label>
            {error && <p className="form-error">{error}</p>}
            <div className="modal-actions">
              <button type="button" className="link quiet" onClick={close}>
                Cancel
              </button>
              <button type="submit" disabled={busy || !message.trim()}>
                {busy ? 'Sending...' : 'Send'}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}
