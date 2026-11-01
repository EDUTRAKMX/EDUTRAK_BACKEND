-- 1. academic_terms: align with the official data dictionary.
--    status (draft/active/closed) is the single source of the term state;
--    is_current was redundant and is removed. name is VARCHAR(80).
DROP INDEX IF EXISTS academic_terms_single_current_idx;
ALTER TABLE academic_terms DROP COLUMN is_current;
ALTER TABLE academic_terms ALTER COLUMN name TYPE VARCHAR(80);
ALTER TABLE academic_terms
    ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'draft',
    ADD CONSTRAINT academic_terms_status_check CHECK (status IN ('draft', 'active', 'closed'));

-- 2. attendance_sessions: lateness configuration copied into each session.
--    Both values must be positive integers (defaults: 15 minutes, 3 lates = 1 absence).
ALTER TABLE attendance_sessions DROP CONSTRAINT attendance_sessions_late_after_minutes_check;
ALTER TABLE attendance_sessions
    ADD CONSTRAINT attendance_sessions_late_after_minutes_check CHECK (late_after_minutes > 0);
-- lates_per_absence already has CHECK (lates_per_absence > 0) from 0003.

-- 3. auth_sessions: record why a session was revoked, so refresh-token reuse
--    detection only fires for tokens that were really rotated.
ALTER TABLE auth_sessions ADD COLUMN revoked_reason VARCHAR(20);
-- Sessions revoked before this migration have an unknown reason; LOGOUT is the
-- conservative choice because it never triggers reuse handling.
UPDATE auth_sessions SET revoked_reason = 'LOGOUT' WHERE revoked_at IS NOT NULL;
ALTER TABLE auth_sessions
    ADD CONSTRAINT auth_sessions_revoked_reason_check
        CHECK (revoked_reason IN ('ROTATED', 'LOGOUT', 'PASSWORD_CHANGED', 'PASSWORD_RESET', 'REUSE_DETECTED')),
    ADD CONSTRAINT auth_sessions_revocation_consistent
        CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL));
