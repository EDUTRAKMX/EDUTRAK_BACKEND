-- Analytics: make the existing alerts table reliable for deterministic, re-evaluable alerts.
--
-- 0004 created alerts(id, student_id, subject_enrollment_id, type, message, evidence, status, created_at).
-- It has no academic context, no way to tell that two evaluations describe the SAME condition,
-- and no timestamps for when an alert was last confirmed or resolved. Added here:
--   group_id      the group (and therefore period) the alert belongs to
--   condition_key identifies the condition inside (student, type), e.g. "unit:<se>:<unit>"
--   updated_at    last time message/evidence/status changed
--   evaluated_at  last time a recalculation CONFIRMED the condition still holds
--   resolved_at   when the condition stopped holding (status RESOLVED)
-- type holds the rule code (GRADE_NA_UNIT, UNJUSTIFIED_ABSENCES, ATTENDANCE_DROP, PENDING_JUSTIFICATIONS).
--
-- Columns are nullable only so rows created before this migration stay valid (none are written
-- by any module); the application always fills them.

ALTER TABLE alerts
    ADD COLUMN group_id      UUID REFERENCES groups (id),
    ADD COLUMN condition_key VARCHAR(160),
    ADD COLUMN updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN evaluated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN resolved_at   TIMESTAMPTZ;

ALTER TABLE alerts
    ADD CONSTRAINT alerts_resolved_consistent CHECK ((status = 'RESOLVED') = (resolved_at IS NOT NULL)),
    ADD CONSTRAINT alerts_type_known CHECK (type IN ('GRADE_NA_UNIT', 'UNJUSTIFIED_ABSENCES', 'ATTENDANCE_DROP', 'PENDING_JUSTIFICATIONS'));

-- At most ONE unresolved alert per condition: two concurrent evaluations cannot both create it.
-- Resolved alerts stay as history (a condition that comes back opens a new row).
CREATE UNIQUE INDEX alerts_one_open_per_condition_idx
    ON alerts (student_id, type, condition_key) WHERE status <> 'RESOLVED';

CREATE INDEX alerts_group_status_idx ON alerts (group_id, status);
