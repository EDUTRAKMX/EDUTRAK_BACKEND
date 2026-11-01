-- Academic: align the tables created in 0002 with the official data dictionary.
-- Forward-only. Column widenings never lose data; the two narrowings (name 200 -> 180)
-- fail loudly, instead of truncating, if a longer value already exists.

-- careers / subjects / subject_units / groups: lengths and subjects.is_active
ALTER TABLE careers ALTER COLUMN code TYPE VARCHAR(30), ALTER COLUMN name TYPE VARCHAR(180);
ALTER TABLE subjects ALTER COLUMN name TYPE VARCHAR(180);
ALTER TABLE subjects ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE subject_units ALTER COLUMN name TYPE VARCHAR(180);
ALTER TABLE groups ALTER COLUMN code TYPE VARCHAR(30);

-- enrollments.created_at -> enrolled_at
ALTER TABLE enrollments RENAME COLUMN created_at TO enrolled_at;

-- schedules.weekday -> day_of_week (1 = Monday ... 7 = Sunday)
ALTER TABLE schedules RENAME COLUMN weekday TO day_of_week;
ALTER TABLE schedules RENAME CONSTRAINT schedules_weekday_check TO schedules_day_of_week_check;

-- subject_enrollments: the dictionary links the student directly to the teaching
-- assignment and adds attempt_number / status. The student's group enrollment is
-- derivable (student + assignment.group_id), so enrollment_id is dropped.
ALTER TABLE subject_enrollments ADD COLUMN student_id UUID REFERENCES users (id);
UPDATE subject_enrollments se SET student_id = e.student_id FROM enrollments e WHERE e.id = se.enrollment_id;
ALTER TABLE subject_enrollments ALTER COLUMN student_id SET NOT NULL;
ALTER TABLE subject_enrollments
    ADD COLUMN attempt_number SMALLINT    NOT NULL DEFAULT 1 CHECK (attempt_number > 0),
    ADD COLUMN status         VARCHAR(20) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'WITHDRAWN'));
ALTER TABLE subject_enrollments DROP COLUMN enrollment_id; -- also drops its UNIQUE with teaching_assignment_id
ALTER TABLE subject_enrollments
    ADD CONSTRAINT subject_enrollments_attempt_key UNIQUE (student_id, teaching_assignment_id, attempt_number);
CREATE INDEX subject_enrollments_student_idx ON subject_enrollments (student_id);
