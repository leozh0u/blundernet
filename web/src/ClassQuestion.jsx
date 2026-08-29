import { useCallback, useEffect, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { useBoardWidth } from './board.js'

// The question a class is currently on.
//
// One component for both roles again, because the server already decides what
// each of them may see: a coach gets the answers gathered by move, a student
// gets their own move and a count. The browser is not trusted to keep that
// straight, it just renders what it was given.
//
// Answers here are legal moves, unlike the coach's board where the rules are
// off. Setting a position up is not a chess question; answering one is, and a
// student who "answers" with a knight teleporting has not answered.

const POLL_MS = 4000

export default function ClassQuestion({ classroomID, role, refreshKey }) {
  const [frame, boardWidth] = useBoardWidth()
  const [question, setQuestion] = useState(null)
  const [answers, setAnswers] = useState([])
  const [board, setBoard] = useState(null)
  const [selected, setSelected] = useState(null)
  const [error, setError] = useState('')
  const coach = role === 'coach'
  // Which question the board was built for, so a poll that returns the same
  // question does not throw away a student's half-made move.
  const builtFor = useRef(null)

  const poll = useCallback(async () => {
    try {
      const res = await fetch(`/api/classrooms/${classroomID}/questions/open`, {
        credentials: 'same-origin',
      })
      if (!res.ok) return
      const body = await res.json()
      setQuestion(body.question)
      setAnswers(body.answers || [])
      if (!body.question) {
        builtFor.current = null
        setBoard(null)
        return
      }
      if (builtFor.current !== body.question.id) {
        builtFor.current = body.question.id
        setBoard(new Chess(body.question.fen))
        setSelected(null)
      }
    } catch {
      // A failed poll is not worth saying anything about. The next one is four
      // seconds away and the screen still holds the last good answer.
    }
  }, [classroomID])

  useEffect(() => {
    poll()
    const id = setInterval(poll, POLL_MS)
    return () => clearInterval(id)
  }, [poll, refreshKey])

  const answer = async (from, to) => {
    if (!question || !board) return false
    // Validated here so the move that reaches the server is a real one, and so
    // an illegal drag snaps back rather than being recorded as an answer.
    const probe = new Chess(board.fen())
    let mv
    try {
      mv = probe.move({ from, to, promotion: 'q' })
    } catch {
      return false
    }
    if (!mv) return false
    const uci = mv.from + mv.to + (mv.promotion || '')
    setError('')
    try {
      const res = await fetch(
        `/api/classrooms/${classroomID}/questions/${question.id}/answer`,
        {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ uci, san: mv.san }),
        },
      )
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error || 'That answer could not be sent.')
        return false
      }
      poll()
    } catch {
      setError('That answer could not be sent.')
      return false
    }
    return true
  }

  const clickSquare = (square) => {
    if (coach || !board) return
    if (selected === square) return setSelected(null)
    if (selected) {
      answer(selected, square)
      setSelected(null)
      return
    }
    if (board.get(square)) setSelected(square)
  }

  const close = async () => {
    await fetch(`/api/classrooms/${classroomID}/questions/${question.id}/close`, {
      method: 'POST',
      credentials: 'same-origin',
    })
    poll()
  }

  if (!question) {
    return coach ? null : (
      <p className="rooms-note">Nothing is being asked right now.</p>
    )
  }

  const turn = board?.turn() === 'b' ? 'Black' : 'White'

  return (
    <div className="question">
      <div className="question-head">
        <h3>{question.prompt || 'What is the best move?'}</h3>
        <span className="question-turn">{turn} to play</span>
      </div>

      <div className="question-body">
        <div className="question-board" ref={frame}>
          {boardWidth > 0 && board && (
            <Chessboard
              id="ClassQuestion"
              boardWidth={boardWidth}
              position={board.fen()}
              boardOrientation={board.turn() === 'b' ? 'black' : 'white'}
              arePiecesDraggable={!coach}
              onPieceDrop={(from, to) => {
                setSelected(null)
                answer(from, to)
                // Always false: the board shows the position being asked about,
                // not the answer. Letting the piece stay where it was dropped
                // would make a student think the position had moved on.
                return false
              }}
              onSquareClick={clickSquare}
              animationDuration={0}
              customBoardStyle={{ borderRadius: '6px' }}
              customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
              customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
              customSquareStyles={
                selected ? { [selected]: { background: 'rgba(246, 200, 92, 0.6)' } } : {}
              }
            />
          )}
        </div>

        <div className="question-side">
          {error && <p className="rooms-error">{error}</p>}

          {coach ? (
            <>
              <p className="question-count">
                {question.answered === 1 ? '1 answer' : `${question.answered} answers`}
              </p>
              {answers.length === 0 ? (
                <p className="coachboard-help">Nobody has answered yet.</p>
              ) : (
                <ul className="question-answers">
                  {answers.map((a) => (
                    <li key={a.uci}>
                      <span className="question-move">{a.san || a.uci}</span>
                      <span className="question-tally">{a.count}</span>
                      <span className="question-who">{a.who.join(', ')}</span>
                    </li>
                  ))}
                </ul>
              )}
              <button className="link quiet" onClick={close}>
                Stop taking answers
              </button>
            </>
          ) : (
            <>
              {question.mine ? (
                <p className="question-mine">
                  You answered <strong>{question.mine}</strong>. Play another move to
                  change it.
                </p>
              ) : (
                <p className="coachboard-help">
                  Play the move you think is best. Only your coach sees the answers.
                </p>
              )}
              <p className="question-count">
                {question.answered === 1
                  ? '1 person has answered'
                  : `${question.answered} people have answered`}
              </p>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
