-- Grades and attendance

-- Classification (NA/SA/DE/AU) is derived in the domain; the score is never rounded.
CREATE TABLE grades (
    id                    UUID         PRIMARY KEY,
    subject_enrollment_id UUID         NOT NULL REFERENCES subject_enrollments (id),
    subject_unit_id       UUID         NOT NULL REFERENCES subject_units (id),
    score                 NUMERIC(5,2) NOT NULL,
    captured_by           UUID         NOT NULL REFERENCES users (id),
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT grades_score_range CHECK (score >= 0.00 AND score <= 100.00),
    UNIQUE (subject_enrollment_id, subject_unit_id)
);
CREATE INDEX grades_subject_unit_id_idx ON grades (subject_unit_id);

-- late_after_minutes / lates_per_absence are copied from configuration when the
-- session is opened, so later config changes do not rewrite history.
CREATE TABLE attendance_sessions (
    id                     UUID        PRIMARY KEY,
    teaching_assignment_id UUID        NOT NULL REFERENCES teaching_assignments (id),
    schedule_id            UUID        REFERENCES schedules (id),
    session_date           DATE        NOT NULL,
    status                 VARCHAR(10) NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CLOSED')),
    opened_by              UUID        NOT NULL REFERENCES users (id),
    opened_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at              TIMESTAMPTZ,
    qr_token_hash          CHAR(64),
    qr_expires_at          TIMESTAMPTZ,
    late_after_minutes     SMALLINT    NOT NULL DEFAULT 15 CHECK (late_after_minutes >= 0),
    lates_per_absence      SMALLINT    NOT NULL DEFAULT 3  CHECK (lates_per_absence > 0),
    CONSTRAINT attendance_sessions_closed_check CHECK ((status = 'CLOSED') = (closed_at IS NOT NULL))
);
CREATE INDEX attendance_sessions_assignment_date_idx ON attendance_sessions (teaching_assignment_id, session_date);

CREATE TABLE attendance_records (
    id                    UUID        PRIMARY KEY,
    attendance_session_id UUID        NOT NULL REFERENCES attendance_sessions (id),
    subject_enrollment_id UUID        NOT NULL REFERENCES subject_enrollments (id),
    status                VARCHAR(10) NOT NULL CHECK (status IN ('PRESENT', 'LATE', 'ABSENT', 'JUSTIFIED')),
    method                VARCHAR(10) NOT NULL CHECK (method IN ('QR', 'NFC', 'MANUAL')),
    recorded_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_by           UUID        NOT NULL REFERENCES users (id),
    ip_address            INET,
    -- Prevents double attendance for the same student in the same session.
    CONSTRAINT attendance_records_unique_per_session UNIQUE (attendance_session_id, subject_enrollment_id)
);
CREATE INDEX attendance_records_subject_enrollment_idx ON attendance_records (subject_enrollment_id);
