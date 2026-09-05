import { useEffect, useRef, useState } from 'react'
import { Chessboard } from 'react-chessboard'
import { Chess } from 'chess.js'
import { useBoardWidth } from './board.js'
import { PositionText } from './Position.jsx'

// What the coach is showing, right now.
//
// This is the board a chess lesson is actually built around: the coach sets a
// position up and talks over it, and the class watches it change. It is not a
// question and there is nothing to answer, which is why it is a separate thing
// from ClassQuestion.
//
// Over a socket rather than polled. A demonstration board polled every few
// seconds is not a demonstration board: the coach says "watch this" and the
// room sees it four seconds later, having already moved on.
export default function ClassLiveBoard({ classroomID }) {
  const [frame, boardWidth] = useBoardWidth()
  const [board, setBoard] = useState(null)
  const [dropped, setDropped] = useState(false)
  const socket = useRef(null)
  const retry = useRef(null)

  useEffect(() => {
    if (!classroomID) return
    let closed = false

    const connect = () => {
      if (closed) return
      const url = `${location.origin.replace(/^http/, 'ws')}/api/classrooms/${classroomID}/board/ws`
      const ws = new WebSocket(url)
      socket.current = ws

      ws.onmessage = (e) => {
        try {
          setBoard(JSON.parse(e.data))
          setDropped(false)
        } catch {
          // A message that will not parse is not worth tearing the socket
          // down for; the next one carries the same board.
        }
      }
      // A lesson runs for an hour on a laptop that sleeps and a phone that
      // changes network, so the socket closing is expected rather than
      // exceptional. Reconnecting keeps the room watching without anybody
      // being told to refresh.
      ws.onclose = () => {
        if (closed) return
        setDropped(true)
        retry.current = setTimeout(connect, 1500)
      }
      ws.onerror = () => ws.close()
    }

    connect()
    return () => {
      closed = true
      clearTimeout(retry.current)
      socket.current?.close()
    }
  }, [classroomID])

  if (!board || !board.live || !board.fen) {
    return (
      <div className="live-board">
        <h3>The board</h3>
        <p className="rooms-note">Nothing on the board yet.</p>
      </div>
    )
  }

  // A coach's board is not required to be a legal position, so the FEN drives
  // the squares directly and chess.js is only asked for a reading of it when
  // it can parse one.
  let readable = null
  try {
    readable = new Chess(board.fen)
  } catch {
    readable = null
  }

  return (
    <div className="live-board">
      <div className="live-head">
        <h3>The board</h3>
        {dropped && <span className="live-dropped">Reconnecting</span>}
      </div>
      {board.caption && <p className="live-caption">{board.caption}</p>}
      <div className="live-frame" ref={frame}>
        {boardWidth > 0 && (
          <Chessboard
            id="ClassLive"
            boardWidth={boardWidth}
            position={board.fen}
            boardOrientation={board.orientation === 'black' ? 'black' : 'white'}
            arePiecesDraggable={false}
            animationDuration={140}
            customBoardStyle={{ borderRadius: '6px' }}
            customDarkSquareStyle={{ backgroundColor: '#567d9f' }}
            customLightSquareStyle={{ backgroundColor: '#e6ecf3' }}
          />
        )}
      </div>
      <PositionText board={readable} label="The board your coach is showing" />
    </div>
  )
}
