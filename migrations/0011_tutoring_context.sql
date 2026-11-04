-- Tutoring follow-ups: group context and append-only history.
--
-- 0004 created tutoring_followups(id, student_id, tutor_id, followup_date, note, created_at).
-- A follow-up has to remember WHICH group (and therefore which period) it belongs to:
-- a student can have enrollments in several groups/periods, and the tutor of a group
-- can change, so student_id + tutor_id alone cannot tell the contexts apart.
--
-- group_id is nullable only so that rows created before this migration stay valid
-- (the module did not exist before, so there are none in practice). The application
-- always sets it. The composite foreign key makes the database guarantee that the
-- student really is (or was) enrolled in that group.

ALTER TABLE tutoring_followups ADD COLUMN group_id UUID REFERENCES groups (id);

ALTER TABLE tutoring_followups
    ADD CONSTRAINT tutoring_followups_enrollment_fkey
    FOREIGN KEY (student_id, group_id) REFERENCES enrollments (student_id, group_id);

-- 0004 only required length > 0 (a note of spaces passed). Any whitespace-only note is empty.
ALTER TABLE tutoring_followups
    ADD CONSTRAINT tutoring_followups_note_not_blank CHECK (note ~ '\S'),
    ADD CONSTRAINT tutoring_followups_note_length CHECK (char_length(note) <= 4000);

-- The history is append-only in the MVP: a new intervention is a new row, and the past
-- is neither edited nor deleted (neither by the API nor by an accidental UPDATE/DELETE).
CREATE FUNCTION tutoring_followups_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'tutoring_followups is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER tutoring_followups_no_update_delete
    BEFORE UPDATE OR DELETE ON tutoring_followups
    FOR EACH ROW EXECUTE FUNCTION tutoring_followups_append_only();

-- History lists are read by group (the current tutor's scope), newest first.
CREATE INDEX tutoring_followups_group_idx ON tutoring_followups (group_id, followup_date DESC, created_at DESC);
