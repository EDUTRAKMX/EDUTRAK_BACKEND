-- Academic: careers, terms, subjects, curriculum, units, groups, enrollments,
-- teaching assignments, schedules, subject enrollments

CREATE TABLE careers (
    id        UUID         PRIMARY KEY,
    code      VARCHAR(20)  NOT NULL UNIQUE,
    name      VARCHAR(200) NOT NULL,
    is_active BOOLEAN      NOT NULL DEFAULT TRUE
);

CREATE TABLE academic_terms (
    id         UUID         PRIMARY KEY,
    name       VARCHAR(100) NOT NULL UNIQUE,
    start_date DATE         NOT NULL,
    end_date   DATE         NOT NULL,
    is_current BOOLEAN      NOT NULL DEFAULT FALSE,
    CONSTRAINT academic_terms_dates_check CHECK (end_date > start_date)
);
-- At most one current term.
CREATE UNIQUE INDEX academic_terms_single_current_idx ON academic_terms (is_current) WHERE is_current;

CREATE TABLE subjects (
    id   UUID         PRIMARY KEY,
    code VARCHAR(30)  NOT NULL UNIQUE,
    name VARCHAR(200) NOT NULL
);

CREATE TABLE curriculum_subjects (
    id          UUID     PRIMARY KEY,
    career_id   UUID     NOT NULL REFERENCES careers (id),
    subject_id  UUID     NOT NULL REFERENCES subjects (id),
    term_number SMALLINT NOT NULL CHECK (term_number > 0),
    UNIQUE (career_id, subject_id)
);
CREATE INDEX curriculum_subjects_subject_id_idx ON curriculum_subjects (subject_id);

CREATE TABLE subject_units (
    id          UUID         PRIMARY KEY,
    subject_id  UUID         NOT NULL REFERENCES subjects (id),
    unit_number SMALLINT     NOT NULL CHECK (unit_number > 0),
    name        VARCHAR(200) NOT NULL,
    UNIQUE (subject_id, unit_number)
);

-- tutor_id: the user (role TUTOR, verified in application) who tutors the group.
CREATE TABLE groups (
    id               UUID        PRIMARY KEY,
    career_id        UUID        NOT NULL REFERENCES careers (id),
    academic_term_id UUID        NOT NULL REFERENCES academic_terms (id),
    code             VARCHAR(20) NOT NULL,
    term_number      SMALLINT    NOT NULL CHECK (term_number > 0),
    tutor_id         UUID        REFERENCES users (id),
    UNIQUE (academic_term_id, code)
);
CREATE INDEX groups_career_id_idx ON groups (career_id);
CREATE INDEX groups_tutor_id_idx ON groups (tutor_id);

CREATE TABLE enrollments (
    id         UUID        PRIMARY KEY,
    student_id UUID        NOT NULL REFERENCES users (id),
    group_id   UUID        NOT NULL REFERENCES groups (id),
    status     VARCHAR(20) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'WITHDRAWN')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (student_id, group_id)
);
CREATE INDEX enrollments_group_id_idx ON enrollments (group_id);

CREATE TABLE teaching_assignments (
    id         UUID PRIMARY KEY,
    group_id   UUID NOT NULL REFERENCES groups (id),
    subject_id UUID NOT NULL REFERENCES subjects (id),
    teacher_id UUID NOT NULL REFERENCES users (id),
    UNIQUE (group_id, subject_id)
);
CREATE INDEX teaching_assignments_teacher_id_idx ON teaching_assignments (teacher_id);

CREATE TABLE schedules (
    id                     UUID        PRIMARY KEY,
    teaching_assignment_id UUID        NOT NULL REFERENCES teaching_assignments (id) ON DELETE CASCADE,
    weekday                SMALLINT    NOT NULL CHECK (weekday BETWEEN 1 AND 7),
    start_time             TIME        NOT NULL,
    end_time               TIME        NOT NULL,
    classroom              VARCHAR(50),
    CONSTRAINT schedules_time_check CHECK (end_time > start_time)
);
CREATE INDEX schedules_teaching_assignment_id_idx ON schedules (teaching_assignment_id);

CREATE TABLE subject_enrollments (
    id                     UUID        PRIMARY KEY,
    enrollment_id          UUID        NOT NULL REFERENCES enrollments (id) ON DELETE CASCADE,
    teaching_assignment_id UUID        NOT NULL REFERENCES teaching_assignments (id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (enrollment_id, teaching_assignment_id)
);
CREATE INDEX subject_enrollments_assignment_idx ON subject_enrollments (teaching_assignment_id);
