// A guest is a real account with a real rating; it just lives in this
// browser's cookie and nothing else. Clear the cookie, use another device, or
// wait for the guest reaper, and it is gone.
//
// Nothing on the site refuses a guest except ranked puzzles, which is the
// right split: ranked is the measured mode and a rating nobody can be held to
// is not a measurement. Everything else is practice, and making somebody sign
// up before they can try a bot game would be asking for a password before
// showing them the product.
//
// The honest cost of that is people building up a rating they do not know is
// temporary. Saying so is cheaper than losing it for them.
//
// The caller passes the whole sentence rather than a subject, because a prop
// dropped into a fixed sentence cannot agree with its own verb: "your best
// run is" and "your rating and games are" need different ones.
export default function GuestNote({ text = 'Your progress is saved to this browser only.' }) {
  const signUp = () =>
    window.dispatchEvent(new CustomEvent('blundernet:auth', { detail: 'signup' }))

  return (
    <p className="guest-note">
      {text}{' '}
      <button type="button" className="link" onClick={signUp}>
        Create an account
      </button>{' '}
      to keep it.
    </p>
  )
}
