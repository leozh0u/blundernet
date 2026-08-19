// Board sounds, synthesised rather than sampled.
//
// Every other chess site ships recorded wood clicks. Doing that here would
// mean sourcing files with a licence that permits commercial redistribution,
// hosting them, and adding a few hundred kilobytes to a bundle that is
// currently 100KB gzipped. The Web Audio API makes these shapes directly, so
// the sounds cost no bytes, no licence and no request.
//
// The shape of a piece landing on a board is a short pitched knock: a fast
// attack, a body around 150 to 250Hz, and a decay under a tenth of a second.
// Everything below is that with different numbers.

const STORAGE_KEY = 'bn_sound'

let ctx = null
let on = read()

function read() {
  try {
    return localStorage.getItem(STORAGE_KEY) !== 'off'
  } catch {
    // Safari in private mode throws on localStorage. Sound on is the sane
    // default and a failed preference is not worth breaking the board over.
    return true
  }
}

// Browsers refuse to start audio without a user gesture, and every call here
// happens after a click or a keypress, so the context is created lazily on the
// first sound rather than at import time. A context made too early is created
// suspended and stays silent even once the user does interact.
function audio() {
  if (!on) return null
  if (!ctx) {
    const AC = window.AudioContext || window.webkitAudioContext
    if (!AC) return null
    ctx = new AC()
  }
  if (ctx.state === 'suspended') ctx.resume()
  return ctx
}

// knock is one percussive note. Frequency slides from `from` to `to` over the
// life of the note, which is what separates a soft placement from a hard
// capture without needing two different instruments.
function knock({ from, to = from, dur = 0.07, gain = 0.22, type = 'triangle', delay = 0 }) {
  const ac = audio()
  if (!ac) return
  const t = ac.currentTime + delay

  const osc = ac.createOscillator()
  osc.type = type
  osc.frequency.setValueAtTime(from, t)
  if (to !== from) osc.frequency.exponentialRampToValueAtTime(to, t + dur)

  // A hard start and an exponential tail. A linear fade reads as a beep; the
  // exponential one reads as something struck.
  const amp = ac.createGain()
  amp.gain.setValueAtTime(gain, t)
  amp.gain.exponentialRampToValueAtTime(0.0001, t + dur)

  osc.connect(amp).connect(ac.destination)
  osc.start(t)
  osc.stop(t + dur + 0.02)
}

export const sound = {
  // A quiet mid knock. This one plays most often, so it is the one that has to
  // stay pleasant after two hundred repetitions.
  move: () => knock({ from: 220, to: 180 }),

  // Lower, louder and longer. Taking a piece should feel heavier than moving.
  capture: () => knock({ from: 190, to: 110, dur: 0.1, gain: 0.28, type: 'sawtooth' }),

  // Two rising notes, because check is information rather than an event and it
  // needs to be distinguishable without being alarming.
  check: () => {
    knock({ from: 440, dur: 0.06, gain: 0.16 })
    knock({ from: 660, dur: 0.08, gain: 0.16, delay: 0.08 })
  },

  // Two knocks close together: the king, then the rook.
  castle: () => {
    knock({ from: 200, to: 170 })
    knock({ from: 200, to: 170, delay: 0.09 })
  },

  // A small rising third for a solved puzzle, and a falling one for a miss.
  // Deliberately short. A fanfare is charming twice and irritating after that.
  solve: () => {
    knock({ from: 523, dur: 0.09, gain: 0.18 })
    knock({ from: 784, dur: 0.14, gain: 0.18, delay: 0.09 })
  },
  fail: () => knock({ from: 300, to: 150, dur: 0.22, gain: 0.2, type: 'sine' }),

  // forMove picks the sound from what the move actually did, so call sites do
  // not each have to reimplement the same three checks. `mv` is a chess.js
  // move and `after` is the position it produced.
  forMove(mv, after) {
    if (!mv) return
    if (mv.flags?.includes('k') || mv.flags?.includes('q')) return sound.castle()
    if (after?.inCheck?.()) return sound.check()
    if (mv.captured) return sound.capture()
    return sound.move()
  },

  enabled: () => on,
  setEnabled(next) {
    on = next
    try {
      localStorage.setItem(STORAGE_KEY, next ? 'on' : 'off')
    } catch {
      // Preference not persisted. It still applies for this session.
    }
    if (next) sound.move() // confirm the choice with the thing being enabled
  },
}
