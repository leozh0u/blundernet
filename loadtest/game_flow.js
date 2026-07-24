// Load test for the arena.
//
// Two scenarios, selected with -e SCENARIO=:
//
//   steady (default) — a constant arrival rate of new games. Use this to
//     measure latency percentiles at a load the fleet can actually hold.
//   ramp             — arrival rate climbing past saturation. Use this to
//     find the ceiling, watch the queue absorb the overflow, and give the
//     worker autoscaling policy something to react to.
//
//   k6 run loadtest/game_flow.js
//   k6 run -e BASE=http://<alb-dns> -e SCENARIO=ramp -e PEAK=40 loadtest/game_flow.js
//
// Each iteration plays one move of one game: create, subscribe over the
// same WebSocket a browser uses, post a move, wait for the engine's reply,
// resign. Move latency is measured from the move request to the engine's
// reply arriving on the socket, so it covers the whole path (api -> redis
// -> sqs -> worker -> mcts -> redis -> pub/sub -> socket) rather than a
// polling interval.

import http from 'k6/http'
import { check } from 'k6'
import { Trend, Rate, Counter } from 'k6/metrics'
import { WebSocket } from 'k6/websockets'

const BASE = __ENV.BASE || 'http://localhost:8080'
const RATE = Number(__ENV.RATE) || 5 // new games per second (steady)
const PEAK = Number(__ENV.PEAK) || 30 // new games per second (ramp target)
const DURATION = __ENV.DURATION || '2m'
const SCENARIO = __ENV.SCENARIO || 'steady'
const MOVE_TIMEOUT_MS = Number(__ENV.MOVE_TIMEOUT_MS) || 30000

// End-to-end time from posting a move to seeing the engine's reply.
const moveLatency = new Trend('engine_move_latency', true)
const moveAnswered = new Rate('engine_move_answered')
const moveTimeouts = new Counter('engine_move_timeouts')

// k6 arrival rates must be whole numbers, so rates are expressed per ten
// seconds. That keeps a tenth of a game per second addressable, which
// matters when one worker only sustains about half a move per second.
const per10s = (rate) => Math.max(1, Math.round(rate * 10))

const scenarios = {
  steady: {
    executor: 'constant-arrival-rate',
    rate: per10s(RATE),
    timeUnit: '10s',
    duration: DURATION,
    preAllocatedVUs: Math.max(20, Math.ceil(RATE * 40)),
    maxVUs: Math.max(60, Math.ceil(RATE * 90)),
  },
  ramp: {
    executor: 'ramping-arrival-rate',
    startRate: per10s(0.5),
    timeUnit: '10s',
    preAllocatedVUs: Math.max(30, Math.ceil(PEAK * 40)),
    maxVUs: Math.max(120, Math.ceil(PEAK * 90)),
    stages: [
      { target: per10s(PEAK / 4), duration: '45s' },
      { target: per10s(PEAK / 2), duration: '45s' },
      { target: per10s(PEAK), duration: '60s' },
      // Hold well past saturation. SQS publishes at one-minute
      // granularity and target tracking wants several breaching points
      // before it acts, so a short spike would end before the fleet
      // could respond to it.
      { target: per10s(PEAK), duration: '6m' },
      { target: per10s(0.5), duration: '30s' }, // drain
    ],
  },
}

export const options = {
  scenarios: { [SCENARIO]: scenarios[SCENARIO] },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{expected_response:true}': ['p(99)<500'],
    engine_move_answered: ['rate>0.95'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
}

const OPENINGS = ['e2e4', 'd2d4', 'g1f3', 'c2c4', 'b1c3', 'e2e3']
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } }

function wsURL(id) {
  return BASE.replace(/^http/, 'ws') + `/api/games/${id}/ws`
}

export default function () {
  const created = http.post(
    `${BASE}/api/games`,
    JSON.stringify({ color: 'white' }),
    { ...JSON_HEADERS, tags: { op: 'create' } },
  )
  if (!check(created, { 'game created': (r) => r.status === 201 })) return

  const id = created.json('id')
  const ws = new WebSocket(wsURL(id))
  let sentAt = 0
  let settled = false
  let guard = null

  const finish = () => {
    if (settled) return
    settled = true
    // Clear the guard, or the pending timer keeps the iteration (and its
    // VU) alive for the full timeout and starves the arrival rate.
    if (guard !== null) clearTimeout(guard)
    http.post(`${BASE}/api/games/${id}/resign`, null, { tags: { op: 'resign' } })
    try {
      ws.close()
    } catch (e) {
      // socket already gone; nothing to clean up
    }
  }

  ws.onopen = () => {
    // The first frame is the current state; the move goes out once we are
    // subscribed, so the engine's reply cannot be missed.
    const uci = OPENINGS[Math.floor(Math.random() * OPENINGS.length)]
    sentAt = Date.now()
    const res = http.post(
      `${BASE}/api/games/${id}/moves`,
      JSON.stringify({ uci }),
      { ...JSON_HEADERS, tags: { op: 'move' } },
    )
    if (!check(res, { 'move accepted': (r) => r.status === 200 })) finish()
  }

  ws.onmessage = (ev) => {
    const state = JSON.parse(ev.data)
    // Two plies means the engine has answered.
    if (sentAt && state.moves && state.moves.length >= 2) {
      moveLatency.add(Date.now() - sentAt)
      moveAnswered.add(true)
      finish()
    }
  }

  ws.onerror = () => {
    if (!settled) {
      moveAnswered.add(false)
      finish()
    }
  }

  // Give up rather than hold a VU forever if the engine never answers.
  guard = setTimeout(() => {
    guard = null
    if (!settled) {
      moveAnswered.add(false)
      moveTimeouts.add(1)
      finish()
    }
  }, MOVE_TIMEOUT_MS)
}
