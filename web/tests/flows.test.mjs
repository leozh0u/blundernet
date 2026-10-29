import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import React from 'react'
import { act, create } from 'react-test-renderer'
import { Chess } from 'chess.js'
import { useDialog } from '../src/lib/dialog.js'

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(path.join(web, 'package.json'))
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const response = (body, status = 200) => ({ ok: status < 400, status, json: async () => body })
const deferred = () => {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

// Keep real components and chess rules; replace only the drag-and-drop renderer.
async function component(file) {
  const result = await build({
    entryPoints: [path.join(web, 'src', file)],
    bundle: true,
    write: false,
    platform: 'node',
    format: 'cjs',
    jsx: 'automatic',
    packages: 'external',
    external: ['react', 'react/jsx-runtime'],
    plugins: [{
      name: 'board-test-surface',
      setup(builder) {
        builder.onResolve({ filter: /^react-chessboard$/ }, () => ({ path: 'board', namespace: 'test' }))
        builder.onLoad({ filter: /.*/, namespace: 'test' }, () => ({
          contents: `import React from 'react';
            export const Chessboard = (props) => React.createElement('chessboard', props);
            export const ChessboardDnDProvider = ({children}) => children;
            export const SparePiece = () => null;`,
        }))
      },
    }],
  })
  const module = { exports: {} }
  new Function('require', 'module', 'exports', result.outputFiles[0].text)(require, module, module.exports)
  return module.exports.default
}

// The browser preference is read at import time; avoid Node's experimental store.
Object.defineProperty(globalThis, 'localStorage', { configurable: true, writable: true, value: { getItem: () => 'off' } })

const [App, Ranked, Streak, Puzzles, MoveInput, Profile] = await Promise.all([
  component('App.jsx'),
  component('features/puzzles/Ranked.jsx'),
  component('features/puzzles/Streak.jsx'),
  component('features/puzzles/Puzzles.jsx'),
  component('components/MoveInput.jsx'),
  component('features/account/Profile.jsx'),
])

function environment(route, handler) {
  const events = new EventTarget()
  const location = { origin: 'http://localhost', host: 'localhost', protocol: 'http:', pathname: route, search: '' }
  globalThis.window = {
    location,
    addEventListener: events.addEventListener.bind(events),
    removeEventListener: events.removeEventListener.bind(events),
    dispatchEvent: events.dispatchEvent.bind(events),
    history: {
      pushState(_state, _title, value) { Object.assign(location, { pathname: new URL(value, location.origin).pathname }) },
      replaceState(_state, _title, value) { Object.assign(location, { pathname: new URL(value, location.origin).pathname }) },
    },
  }
  globalThis.location = location
  globalThis.localStorage = { getItem: () => 'off' }
  globalThis.ResizeObserver = class { observe() {} disconnect() {} }
  globalThis.WebSocket = class {
    static last
    constructor() { globalThis.WebSocket.last = this }
    close() {}
  }
  globalThis.fetch = async (url, options) => {
    const matched = handler(url, options)
    if (matched !== undefined) return matched
    if (url === '/api/auth/me') return response({ user: null })
    if (url === '/api/me/profile') return response(null)
    if (url.startsWith('/api/me/games')) return response({ games: [] })
    if (url === '/api/stats') return response({ total: 0 })
    if (url === '/api/puzzles/openings') return response({ openings: [] })
    if (url === '/api/puzzles/ranked/me') return response({ rating: 1000, solved: 0 })
    throw new Error(`Unexpected request: ${url}`)
  }
}

async function mount(Component) {
  let tree
  await act(async () => {
    tree = create(React.createElement(Component), {
      createNodeMock: () => ({ getBoundingClientRect: () => ({ width: 480 }) }),
    })
    await sleep(0)
  })
  return tree
}

const board = (tree) => tree.root.findByType('chessboard')
const button = (tree, text) => tree.root.findAllByType('button').find((node) => node.children.join('') === text)
async function drop(tree, from, to) {
  let accepted
  await act(async () => {
    accepted = board(tree).props.onPieceDrop(from, to)
    await sleep(0)
  })
  return accepted
}
const line = ['e7e5', 'g1f3', 'b8c6', 'f1b5', 'a7a6']
function position(moves) {
  const chess = new Chess()
  for (const move of moves) chess.move(move)
  return chess.fen()
}
const resumed = { id: 'puzzle', fen: position(['e4', 'e5', 'Nf3']), step: 2, color: 'black', count: 1, best: 2 }

test('ranked resumes its current position, blocks overlapping moves and replays only the remaining answer', async () => {
  let sent = 0
  environment('/puzzles/ranked', (url) => {
    if (url === '/api/puzzles/ranked') return response(resumed)
    if (url === '/api/puzzles/ranked/move') return response(++sent === 1
      ? { correct: true, done: false, reply: 'f1b5' }
      : { correct: false, done: true, solution: line, result: { change: -5 } })
  })
  const tree = await mount(Ranked)
  try {
    assert.equal(board(tree).props.position, resumed.fen)
    assert.equal(await drop(tree, 'b8', 'c6'), true)
    assert.equal(board(tree).props.arePiecesDraggable, false)
    assert.equal(await drop(tree, 'f1', 'b5'), false)
    await act(async () => { await sleep(370) })
    assert.equal(board(tree).props.arePiecesDraggable, true)
    assert.equal(await drop(tree, 'd7', 'd6'), true)
    assert.equal(sent, 2)
    assert.ok(tree.root.findAllByType('button').some((node) => node.props.className?.includes('answer-move') && node.findAllByProps({ className: 'answer-san' }).some((san) => san.children[0] === 'a6')))
  } finally { act(() => tree.unmount()) }
})

test('a failed ranked request restores the board and offers authoritative resume', async () => {
  environment('/puzzles/ranked', (url) => {
    if (url === '/api/puzzles/ranked') return response(resumed)
    if (url === '/api/puzzles/ranked/move') return Promise.reject(new Error('offline'))
  })
  const tree = await mount(Ranked)
  try {
    await drop(tree, 'b8', 'c6')
    assert.equal(board(tree).props.position, resumed.fen)
    assert.equal(board(tree).props.arePiecesDraggable, false)
    assert.ok(button(tree, 'Resume puzzle'))
  } finally { act(() => tree.unmount()) }
})

test('streak resumes a progressed puzzle and locks input until the forced reply finishes', async () => {
  environment('/puzzles/streak', (url) => {
    if (url === '/api/puzzles/streak') return response(resumed)
    if (url === '/api/puzzles/streak/move') return response({ correct: true, done: false, reply: 'f1b5', count: 1 })
  })
  const tree = await mount(Streak)
  try {
    await act(async () => { button(tree, 'Start a run').props.onClick(); await sleep(0) })
    assert.equal(board(tree).props.position, resumed.fen)
    await drop(tree, 'b8', 'c6')
    assert.equal(board(tree).props.arePiecesDraggable, false)
    assert.equal(await drop(tree, 'f1', 'b5'), false)
    await act(async () => { await sleep(370) })
    assert.equal(board(tree).props.arePiecesDraggable, true)
    assert.equal(board(tree).props.position, position(['e4', 'e5', 'Nf3', 'Nc6', 'Bb5']))
  } finally { act(() => tree.unmount()) }
})

test('failed optimistic game moves reconcile with the server instead of leaving a phantom move', async () => {
  const game = { id: 'game', fen: new Chess().fen(), status: 'ongoing', turn: 'white', player_color: 'white', moves: [] }
  environment('/play', (url, options) => {
    if (url === '/api/games' && options?.method === 'POST') return response(game)
    if (url === '/api/games/game/moves') return response({ error: 'Move refused' }, 409)
    if (url === '/api/games/game') return response(game)
  })
  const tree = await mount(App)
  try {
    await act(async () => { tree.root.findByProps({ className: 'choice light' }).props.onClick(); await sleep(0) })
    await drop(tree, 'e2', 'e4')
    assert.equal(board(tree).props.position, game.fen)
    assert.ok(tree.root.findByProps({ className: 'error' }).children.includes('Move refused'))
  } finally { act(() => tree.unmount()) }
})

test('a late move HTTP response cannot overwrite a newer engine reply', async () => {
  const game = { id: 'game', fen: new Chess().fen(), status: 'ongoing', turn: 'white', player_color: 'white', moves: [] }
  const pending = deferred()
  environment('/play', (url, options) => {
    if (url === '/api/games' && options?.method === 'POST') return response(game)
    if (url === '/api/games/game/moves') return pending.promise
  })
  const tree = await mount(App)
  try {
    await act(async () => { tree.root.findByProps({ className: 'choice light' }).props.onClick(); await sleep(0) })
    await drop(tree, 'e2', 'e4')
    const newer = { ...game, fen: position(['e4', 'e5']), moves: ['e2e4', 'e7e5'] }
    await act(async () => {
      WebSocket.last.onmessage({ data: JSON.stringify(newer) })
      pending.resolve(response({ ...game, fen: position(['e4']), turn: 'black', moves: ['e2e4'] }))
      await sleep(0)
    })
    assert.equal(board(tree).props.position, newer.fen)
  } finally { act(() => tree.unmount()) }
})

test('learning filters ignore stale results and lock the forced reply', async () => {
  const older = deferred()
  const puzzle = { id: 'new', fen: new Chess().fen(), setup_move: 'e2e4', color: 'black', solution: line, themes: [], phase: 'opening' }
  environment('/', (url) => {
    if (url === '/api/puzzles?limit=10') return older.promise
    if (url.startsWith('/api/puzzles?rating_max=1200')) return response({ puzzles: [puzzle] })
  })
  const tree = await mount(Puzzles)
  try {
    await act(async () => { button(tree, 'Under 1200').props.onClick(); await sleep(0) })
    await act(async () => { older.resolve(response({ puzzles: [{ ...puzzle, id: 'old', setup_move: 'd2d4' }] })); await sleep(650) })
    assert.equal(board(tree).props.position, position(['e4']))
    await drop(tree, 'e7', 'e5')
    assert.equal(board(tree).props.arePiecesDraggable, false)
    assert.equal(await drop(tree, 'g1', 'f3'), false)
    assert.equal(button(tree, 'Hint').props.disabled, true)
    await act(async () => { await sleep(370) })
    assert.equal(board(tree).props.arePiecesDraggable, true)
  } finally { act(() => tree.unmount()) }
})

test('dialogs trap Tab in both directions, dismiss with Escape and restore the launcher', async () => {
  const handlers = new Map()
  const control = () => ({
    disabled: false, tabIndex: 0, isConnected: true,
    getClientRects: () => [{}],
    focus() { document.activeElement = this },
  })
  const launcher = control()
  const first = control()
  const last = control()
  globalThis.document = {
    activeElement: launcher,
    addEventListener: (name, fn) => handlers.set(name, fn),
    removeEventListener: (name) => handlers.delete(name),
  }
  let closed = false
  function Dialog() {
    return React.createElement('div', { ref: useDialog(true, () => { closed = true }) })
  }
  let tree
  await act(async () => {
    tree = create(React.createElement(Dialog), { createNodeMock: () => ({
      contains: (node) => node === first || node === last,
      querySelectorAll: () => [first, last],
    }) })
  })
  try {
    assert.equal(document.activeElement, first)
    const key = (value, shiftKey = false) => handlers.get('keydown')({ key: value, shiftKey, preventDefault() {} })
    key('Tab', true)
    assert.equal(document.activeElement, last)
    key('Tab')
    assert.equal(document.activeElement, first)
    key('Escape')
    assert.equal(closed, true)
  } finally {
    act(() => tree.unmount())
    assert.equal(document.activeElement, launcher)
    delete globalThis.document
  }
})

test('keyboard move entry validates squares, normalises case and reports illegal moves', async () => {
  const sent = []
  let accepted = true
  let tree
  await act(async () => { tree = create(React.createElement(MoveInput, { onMove: (...squares) => { sent.push(squares); return accepted } })) })
  const type = async (value) => act(async () => tree.root.findByType('input').props.onChange({ target: { value } }))
  const submit = async () => act(async () => tree.root.findByType('form').props.onSubmit({ preventDefault() {} }))
  try {
    await type('E7E8')
    await submit()
    assert.deepEqual(sent, [['e7', 'e8']])
    assert.equal(tree.root.findByType('input').props.value, '')
    await type('e9e4')
    await submit()
    assert.equal(sent.length, 1)
    accepted = false
    await type('e2e5')
    await submit()
    assert.match(tree.root.findByProps({ role: 'status' }).children.join(''), /not available/)
    await act(async () => { tree.update(React.createElement(MoveInput, { disabled: true, onMove: () => assert.fail('disabled move submitted') })) })
    assert.equal(tree.root.findByType('input').props.readOnly, true)
    await submit()
  } finally { act(() => tree.unmount()) }
})

test('the account renders its weakness report and passes its theme to the drill', async () => {
  environment('/me', (url) => {
    if (url === '/api/me/weaknesses') return response({ attempts: 30, themes: [
      { theme: 'fork', verdict: 'weak', rate: 0.2, baseline: 0.8, attempts: 10 },
    ] })
  })
  let chosen
  let tree
  await act(async () => {
    tree = create(React.createElement(Profile, { onTheme: (theme) => { chosen = theme } }))
    await sleep(0)
  })
  try {
    assert.ok(tree.root.findAllByType('h2').some((heading) => heading.children[0] === 'What to work on'))
    act(() => button(tree, 'practise').props.onClick())
    assert.equal(chosen, 'fork')
  } finally { act(() => tree.unmount()) }
})

test('an alternative mating move is shown and recorded as the move the player chose', async () => {
  const puzzle = { id: 'mate', fen: '8/5K1k/6Q1/8/8/8/8/8 b - - 0 1', setup_move: 'h7h8', color: 'white',
    solution: ['g6g7'], themes: ['mateIn1'], phase: 'endgame' }
  let solved
  environment('/', (url, options) => {
    if (url === '/api/puzzles?limit=10') return response({ puzzles: [puzzle] })
    if (url === '/api/puzzles/mate/attempt') { solved = JSON.parse(options.body).solved; return response({}) }
  })
  const tree = await mount(Puzzles)
  try {
    await act(async () => { await sleep(620) })
    assert.equal(await drop(tree, 'g6', 'g8'), true)
    assert.equal(new Chess(board(tree).props.position).get('g8')?.type, 'q')
    assert.equal(solved, true)
    assert.ok(tree.root.findAllByProps({ className: 'answer-san' }).some((node) => node.children[0] === 'Qg8#'))
  } finally { act(() => tree.unmount()) }
})
