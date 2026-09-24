import { useEffect, useRef, useState } from 'react'

// The one-time recovery code panel.
//
// This is the only screen on the site where closing it loses something that
// cannot be recovered. The code exists in plaintext exactly once, in this
// response, and the server keeps only an Argon2id hash of it. So the panel is
// built around the one job of getting the code out of the browser and into
// somewhere the user will still have it in six months: it states the stakes
// before the code, it makes copying one click, and it will not let itself be
// dismissed until the user says they have it.
//
// The confirmation is a real gate rather than a nag. A user who clicks past
// this has no way back into their account, so an interruption here is the
// cheapest it will ever be.
export default function RecoveryCode({ code, heading, blurb, onDone, doneLabel = 'I have saved it' }) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)
  const codeRef = useRef(null)
  const timer = useRef(null)

  useEffect(() => () => clearTimeout(timer.current), [])

  async function copy() {
    setCopyFailed(false)
    try {
      // navigator.clipboard is unavailable over plain http and can be denied
      // by permission policy, so the failure path is a real one rather than
      // defensive decoration: it selects the code so the user can copy it the
      // ordinary way.
      if (!navigator.clipboard) throw new Error('no clipboard')
      await navigator.clipboard.writeText(code)
      setCopied(true)
      clearTimeout(timer.current)
      timer.current = setTimeout(() => setCopied(false), 2500)
    } catch {
      setCopyFailed(true)
      selectCode()
    }
  }

  function selectCode() {
    const node = codeRef.current
    if (!node) return
    const range = document.createRange()
    range.selectNodeContents(node)
    const sel = window.getSelection()
    sel.removeAllRanges()
    sel.addRange(range)
  }

  return (
    <div className="recovery">
      <h2>{heading}</h2>
      <p className="note">{blurb}</p>

      {/* Monospace because this is a value to be transcribed character by
          character, which is what the font is actually for. Selectable and
          click-to-select, because copy buttons fail and people still need the
          text.

          Rendered as its five groups rather than one string. At a narrow
          width the line has to break somewhere, and a break inside a group is
          how somebody copies the code down wrong. Breaking between groups is
          both safe and legible, so the markup makes the safe break the only
          one available. The screen reader gets the characters spaced out,
          because "RJF1V" read as a word is useless to transcribe. */}
      <code
        className="recovery-code"
        ref={codeRef}
        onClick={selectCode}
        tabIndex={0}
        onFocus={selectCode}
        aria-label={`Your recovery code is ${code.split('').join(' ')}`}
      >
        {code.split('-').map((group, i, all) => (
          <span className="recovery-group" key={i}>
            {group}
            {i < all.length - 1 && <span className="recovery-sep">-</span>}
          </span>
        ))}
      </code>

      <div className="recovery-actions">
        <button type="button" onClick={copy}>
          {copied ? 'Copied' : 'Copy'}
        </button>
        {copyFailed && (
          <span className="recovery-hint" role="status">
            Could not copy for you. It is selected, so press Cmd or Ctrl and C.
          </span>
        )}
        {copied && (
          <span className="recovery-hint" role="status">
            Now paste it somewhere you will still have later.
          </span>
        )}
      </div>

      <label className="recovery-ack">
        <input
          type="checkbox"
          checked={acknowledged}
          onChange={(e) => setAcknowledged(e.target.checked)}
        />
        <span>I have saved this code somewhere safe.</span>
      </label>

      <div className="modal-actions">
        <button type="button" disabled={!acknowledged} onClick={onDone}>
          {doneLabel}
        </button>
      </div>
    </div>
  )
}
