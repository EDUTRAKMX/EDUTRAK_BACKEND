-- Justifications, tutoring, analytics, audit

CREATE TABLE justification_requests (
    id                   UUID        PRIMARY KEY,
    attendance_record_id UUID        NOT NULL REFERENCES attendance_records (id),
    student_id           UUID        NOT NULL REFERENCES users (id),
    reason               TEXT        NOT NULL CHECK (length(reason) > 0),
    status               VARCHAR(20) NOT NULL DEFAULT 'PENDING_TUTOR'
        CHECK (status IN ('PENDING_TUTOR', 'TUTOR_REJECTED', 'PENDING_TEACHER', 'TEACHER_REJECTED', 'JUSTIFIED')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One open or approved request per absence; a rejected one may be followed by a new request.
CREATE UNIQUE INDEX justification_requests_active_per_record_idx
    ON justification_requests (attendance_record_id)
    WHERE status IN ('PENDING_TUTOR', 'PENDING_TEACHER', 'JUSTIFIED');
CREATE INDEX justification_requests_student_idx ON justification_requests (student_id);
CREATE INDEX justification_requests_status_idx ON justification_requests (status);

-- Files are private; only metadata and the storage key live in the database.
CREATE TABLE justification_evidence (
    id                       UUID         PRIMARY KEY,
    justification_request_id UUID         NOT NULL REFERENCES justification_requests (id) ON DELETE CASCADE,
    storage_key              VARCHAR(255) NOT NULL UNIQUE,
    original_filename        VARCHAR(255) NOT NULL,
    mime_type                VARCHAR(100) NOT NULL
        CHECK (mime_type IN ('application/pdf', 'image/jpeg', 'image/png')),
    size_bytes               INTEGER      NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX justification_evidence_request_idx ON justification_evidence (justification_request_id);

-- Append-only decision history.
CREATE TABLE justification_reviews (
    id                       UUID        PRIMARY KEY,
    justification_request_id UUID        NOT NULL REFERENCES justification_requests (id),
    reviewer_id              UUID        NOT NULL REFERENCES users (id),
    stage                    VARCHAR(10) NOT NULL CHECK (stage IN ('TUTOR', 'TEACHER')),
    decision                 VARCHAR(10) NOT NULL CHECK (decision IN ('APPROVED', 'REJECTED')),
    comment                  TEXT,
    reviewed_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX justification_reviews_request_idx ON justification_reviews (justification_request_id);

CREATE TABLE tutoring_followups (
    id            UUID        PRIMARY KEY,
    student_id    UUID        NOT NULL REFERENCES users (id),
    tutor_id      UUID        NOT NULL REFERENCES users (id),
    followup_date DATE        NOT NULL,
    note          TEXT        NOT NULL CHECK (length(note) > 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tutoring_followups_student_idx ON tutoring_followups (student_id, followup_date DESC);
CREATE INDEX tutoring_followups_tutor_idx ON tutoring_followups (tutor_id);

-- evidence holds objective figures, e.g. previous_pct and current_pct of attendance.
CREATE TABLE alerts (
    id                    UUID        PRIMARY KEY,
    student_id            UUID        NOT NULL REFERENCES users (id),
    subject_enrollment_id UUID        REFERENCES subject_enrollments (id),
    type                  VARCHAR(50) NOT NULL,
    message               TEXT        NOT NULL,
    evidence              JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status                VARCHAR(15) NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ACKNOWLEDGED', 'RESOLVED')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX alerts_student_status_idx ON alerts (student_id, status);

CREATE TABLE audit_logs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id UUID        REFERENCES users (id),
    action        VARCHAR(80) NOT NULL,
    entity_type   VARCHAR(50) NOT NULL,
    entity_id     VARCHAR(64),
    metadata      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_entity_idx ON audit_logs (entity_type, entity_id);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_user_id, created_at DESC);
