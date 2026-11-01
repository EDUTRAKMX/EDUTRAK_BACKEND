-- Align academic level columns with the official data dictionary.

-- curriculum_subjects: PK is (career_id, subject_id, level); the surrogate id and
-- the (career_id, subject_id) uniqueness of 0002 go away. Nothing references
-- curriculum_subjects.id.
ALTER TABLE curriculum_subjects RENAME COLUMN term_number TO level;
ALTER TABLE curriculum_subjects RENAME CONSTRAINT curriculum_subjects_term_number_check TO curriculum_subjects_level_check;
ALTER TABLE curriculum_subjects DROP CONSTRAINT curriculum_subjects_career_id_subject_id_key;
ALTER TABLE curriculum_subjects DROP CONSTRAINT curriculum_subjects_pkey;
ALTER TABLE curriculum_subjects DROP COLUMN id;
ALTER TABLE curriculum_subjects ADD PRIMARY KEY (career_id, subject_id, level);

-- groups: term_number -> level SMALLINT NOT NULL.
ALTER TABLE groups RENAME COLUMN term_number TO level;
ALTER TABLE groups RENAME CONSTRAINT groups_term_number_check TO groups_level_check;
