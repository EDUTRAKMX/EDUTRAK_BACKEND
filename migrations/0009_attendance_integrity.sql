-- Attendance integrity.
-- 1. At most ONE open session per teaching assignment (a second concurrent start gets a unique violation).
CREATE UNIQUE INDEX attendance_sessions_one_open_per_assignment_idx
    ON attendance_sessions (teaching_assignment_id) WHERE status = 'OPEN';

-- 2. QR state is consistent: hash and expiry are set together, and a closed session holds no QR.
ALTER TABLE attendance_sessions
    ADD CONSTRAINT attendance_sessions_qr_pair_check CHECK ((qr_token_hash IS NULL) = (qr_expires_at IS NULL)),
    ADD CONSTRAINT attendance_sessions_closed_no_qr_check CHECK (status = 'OPEN' OR qr_token_hash IS NULL);
