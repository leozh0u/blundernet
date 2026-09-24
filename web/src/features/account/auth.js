// Auth and profile calls. Cookies carry the session, so every request needs
// credentials: 'same-origin' and nothing here handles a token by hand.

async function post(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return null
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || 'Something went wrong.')
  return data
}

async function get(path) {
  const res = await fetch(path, { credentials: 'same-origin' })
  if (!res.ok) return null
  return res.json()
}

export const auth = {
  // Returns null when nobody is signed in, and a user with guest: true for
  // someone who has played but not registered.
  me: () => get('/api/auth/me').then((d) => d?.user ?? null),
  profile: () => get('/api/me/profile'),
  history: () => get('/api/me/games').then((d) => d?.games ?? []),
  signup: (username, password) => post('/api/auth/signup', { username, password }),
  login: (username, password) => post('/api/auth/login', { username, password }),
  logout: () => post('/api/auth/logout'),

  // Account recovery without email. The code is a bearer credential, so these
  // go through the same rate limiter as login.
  recover: (username, code, password) => post('/api/auth/recover', { username, code, password }),
  newRecoveryCode: (password) => post('/api/auth/recovery-code', { password }),
}

// The server accepts a code in any case and ignores the grouping dashes, so
// the field can be forgiving too. Kept here as well as on the server because
// showing the user their own input tidied up is a better signal than silently
// accepting a mess.
export function tidyRecoveryCode(raw) {
  const chars = raw.toUpperCase().replace(/[^0-9A-HJKMNP-TV-Z]/g, '').slice(0, 25)
  return chars.replace(/(.{5})(?=.)/g, '$1-')
}
