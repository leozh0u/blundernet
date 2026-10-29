-- A delivery/retry of the same ranked or streak result must count only once.
-- NULL keeps learning attempts and existing rows unchanged.
ALTER TABLE puzzle_attempts ADD COLUMN attempt_id UUID UNIQUE;
