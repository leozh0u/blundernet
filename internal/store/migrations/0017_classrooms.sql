-- Classrooms: a coach, a join code, and a roster whose puzzle work the coach
-- can see. Built for a chess team using this in training, which is the case
-- that decides most of the shape below.
--
-- Membership is its own table with a role rather than a students list plus an
-- owner column. A club with two coaches is the first thing that breaks the
-- owner-only version, and it breaks it in the worst place, authorization, by
-- forcing a second rule for the second coach.
-- The id is generated in Go, as user ids already are, rather than by a
-- database default. It keeps id generation in one place and off any extension.
CREATE TABLE classrooms (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
    -- Normalised: upper case, no dashes. The dashes exist so a code can be
    -- read off a whiteboard, and what gets compared has to be the form typing
    -- produces rather than the pretty one.
    join_code  TEXT NOT NULL UNIQUE,
    -- Kept alongside the coach rows so a classroom always has someone to
    -- attribute it to, including after every coach has left.
    created_by UUID REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per person in the room. ON DELETE CASCADE both ways: deleting a
-- classroom should not leave rows pointing at nothing, and a deleted account
-- should leave the roster rather than appear as a ghost the coach cannot
-- remove.
CREATE TABLE classroom_members (
    classroom_id UUID NOT NULL REFERENCES classrooms (id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('coach', 'student')),
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (classroom_id, user_id)
);

-- "Which rooms am I in" is on every page load of the classroom section, and
-- the primary key above is the wrong way round for it.
CREATE INDEX classroom_members_user_idx ON classroom_members (user_id);
