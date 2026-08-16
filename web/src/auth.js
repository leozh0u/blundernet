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
}

// Ratings are float64 on the wire because Glicko-2 works in fractions. Nobody
// wants to read 953.9138774304494.
export function displayRating(profile) {
  if (!profile) return null
  return Math.round(profile.rating)
}
