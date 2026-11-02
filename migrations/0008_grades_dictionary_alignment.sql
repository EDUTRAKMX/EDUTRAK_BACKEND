-- Grades: align column names with the official data dictionary.
-- score NUMERIC(5,2), the 0..100 CHECK and UNIQUE (subject_enrollment_id, subject_unit_id)
-- already exist from 0003. The category (NA/SA/DE/AU) is derived from score, never stored.
ALTER TABLE grades RENAME COLUMN captured_by TO recorded_by;
ALTER TABLE grades RENAME COLUMN created_at TO recorded_at;
ALTER TABLE grades RENAME CONSTRAINT grades_captured_by_fkey TO grades_recorded_by_fkey;
