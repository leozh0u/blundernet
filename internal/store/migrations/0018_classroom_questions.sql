-- Asking the class a question about a position, and collecting the answers.
--
-- This is the interactive half of a classroom. The coach sets a position up on
-- their board, asks "what is the best move here", and every student answers by
-- playing a move. The coach then sees the answers gathered by move, which is
-- the thing a coach actually wants: not who is right, but what the wrong
-- answers were and how many people gave each one. Three students playing the
-- same losing capture is a lesson; one student getting it wrong is a typo.
--
-- The answer is a move rather than free text on purpose. A move can be counted,
-- grouped and shown on a board. A sentence can only be read, and a coach with
-- twenty students will not read twenty sentences.
CREATE TABLE classroom_questions (
    id           UUID PRIMARY KEY,
    classroom_id UUID NOT NULL REFERENCES classrooms (id) ON DELETE CASCADE,
    asked_by     UUID REFERENCES users (id) ON DELETE SET NULL,
    -- The position as it stood when the question was asked. Stored rather than
    -- referenced, because the coach's board keeps moving and a question has to
    -- stay the question that was asked.
    fen          TEXT NOT NULL,
    prompt       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Closed rather than deleted, so a class can look back at what was asked.
    closed_at    TIMESTAMPTZ
);

-- The open question for a room, which is what both sides poll for.
CREATE INDEX classroom_questions_open_idx
    ON classroom_questions (classroom_id, created_at DESC);

-- One answer per student per question, and answering again replaces it. A
-- student who spots their mistake before the coach reveals should be able to
-- change their mind; that is what thinking looks like.
CREATE TABLE classroom_answers (
    question_id UUID NOT NULL REFERENCES classroom_questions (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    uci         TEXT NOT NULL,
    san         TEXT NOT NULL DEFAULT '',
    answered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (question_id, user_id)
);
