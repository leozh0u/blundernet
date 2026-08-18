// Load test for the puzzle side, which is now the front page and therefore the
// thing a launch actually points at. The game flow test next to this one
// measures the engine path; this one measures the search.
//
//   k6 run loadtest/puzzles.js
//   k6 run -e BASE=https://blundernet.com -e RATE=20 -e DURATION=1m loadtest/puzzles.js
//
// Each iteration is one drill fetch, with the filters a real session sends
// rather than the same query every time. Repeating one query would measure the
// page cache; the point is to measure the sampler across the corpus.
//
// MIX picks which population of filters to send, and the two answer different
// questions.
//
//   MIX=real   (default) what the site is actually asked for. The filter panel
//              starts empty, so most fetches carry no filter at all, and a
//              theme is something a minority of sessions clicks into.
//   MIX=stress a theme on nearly every request. This is the expensive path:
//              the theme is a recheck against the heap, common themes sit
//              about one row in ten to one in thirty-six, so the scan walks
//              tens of random pages per puzzle it keeps.
//
// Reporting only the stress number understates the site, and reporting only
// the real number hides the cliff. Run both.

import http from 'k6/http'
import { check } from 'k6'
import { Trend } from 'k6/metrics'

const BASE = __ENV.BASE || 'http://localhost:8080'
const RATE = Number(__ENV.RATE) || 10 // searches per second
const DURATION = __ENV.DURATION || '1m'
const MIX = __ENV.MIX || 'real'

const searchLatency = new Trend('puzzle_search_latency', true)

export const options = {
  scenarios: {
    search: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.max(10, RATE * 2),
      maxVUs: Math.max(30, RATE * 6),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'puzzle_search_latency': ['p(95)<800'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
}

// The bands, lengths and themes the filter UI actually offers.
const BANDS = [[0, 1200], [1200, 1500], [1500, 1800], [1800, 2100], [2100, 2400], [0, 0]]
const MOVES = [0, 1, 2, 3, 4]
const THEMES = ['', 'fork', 'pin', 'skewer', 'backRankMate', 'sacrifice', 'mateIn2', 'deflection']
const PHASES = ['', 'opening', 'middlegame', 'endgame']

const pick = (a) => a[Math.floor(Math.random() * a.length)]

// realFilter models a session rather than a permutation. The panel opens
// empty and most fetches are the refill behind the queue, so the common case
// is no filter at all. The shares below are a judgement, not a measurement:
// there is no analytics on the site yet, so they are set from what the UI
// makes easy rather than from what people did.
function realFilter() {
  const r = Math.random()
  if (r < 0.55) return { band: [0, 0], moves: 0, theme: '', phase: '' } // untouched panel
  if (r < 0.75) return { band: pick(BANDS), moves: 0, theme: '', phase: '' } // rating only
  if (r < 0.90) return { band: pick(BANDS), moves: pick(MOVES), theme: '', phase: pick(PHASES) }
  return { band: pick(BANDS), moves: pick(MOVES), theme: pick(THEMES.slice(1)), phase: pick(PHASES) }
}

// stressFilter is every knob turned at once, theme included on 7 of 8.
function stressFilter() {
  return { band: pick(BANDS), moves: pick(MOVES), theme: pick(THEMES), phase: pick(PHASES) }
}

export default function () {
  const { band, moves, theme, phase } = MIX === 'stress' ? stressFilter() : realFilter()

  const q = ['limit=10']
  if (band[0]) q.push(`rating_min=${band[0]}`)
  if (band[1]) q.push(`rating_max=${band[1]}`)
  if (moves) q.push(`moves_min=${moves}`, `moves_max=${moves}`)
  if (theme) q.push(`theme=${theme}`)
  if (phase) q.push(`phase=${phase}`)

  const res = http.get(`${BASE}/api/puzzles?${q.join('&')}`, { tags: { name: 'search' } })
  searchLatency.add(res.timings.duration)

  const body = res.status === 200 ? res.json() : null
  check(res, {
    'search answered': (r) => r.status === 200,
    // An empty answer is a filter combination the corpus cannot serve, which
    // is a real outcome. What must not happen is an error or a hang.
    'search returned puzzles or an honest empty list': () => body !== null && Array.isArray(body.puzzles),
  })
}
