import { useState } from 'react'

// Coordinate entry makes the same move handler available without dragging.
export default function MoveInput({ onMove, disabled = false }) {
  const [move, setMove] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event) => {
    event.preventDefault()
    if (disabled || busy || !/^[a-h][1-8][a-h][1-8]$/.test(move)) return
    setBusy(true)
    setError('')
    try {
      if (await onMove(move.slice(0, 2), move.slice(2))) setMove('')
      else setError('That move is not available. Check the squares and whose turn it is.')
    } catch {
      setError('That move could not be sent. Try again.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="move-entry" onSubmit={submit}>
      <label>
        Move
        <input value={move} onChange={(event) => setMove(event.target.value.toLowerCase().replace(/\s/g, ''))}
          placeholder="e2e4" maxLength={4} pattern="[a-h][1-8][a-h][1-8]" autoComplete="off"
          spellCheck={false} readOnly={disabled || busy} aria-disabled={disabled || busy} required />
      </label>
      <button type="submit" className="ghost" disabled={disabled || busy || move.length !== 4}>Play</button>
      {error && <p className="form-error" role="status">{error}</p>}
    </form>
  )
}
