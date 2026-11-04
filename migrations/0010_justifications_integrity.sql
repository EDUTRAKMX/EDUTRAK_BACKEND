-- Justifications integrity.
-- 1. A request gets at most ONE review per stage (TUTOR, TEACHER): a stage cannot be decided twice,
--    and a later stage can never overwrite an earlier decision.
ALTER TABLE justification_reviews
    ADD CONSTRAINT justification_reviews_one_per_stage UNIQUE (justification_request_id, stage);

-- 2. The decision history is append-only: no UPDATE and no DELETE, whatever the caller.
CREATE FUNCTION justification_reviews_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'justification_reviews is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER justification_reviews_no_update_delete
    BEFORE UPDATE OR DELETE ON justification_reviews
    FOR EACH ROW EXECUTE FUNCTION justification_reviews_append_only();

-- 3. Rejecting needs a reason; the request text cannot be blank (any whitespace counts as blank).
ALTER TABLE justification_reviews
    ADD CONSTRAINT justification_reviews_reject_comment_check
        CHECK (decision = 'APPROVED' OR coalesce(comment, '') ~ '\S');
ALTER TABLE justification_requests
    ADD CONSTRAINT justification_requests_reason_nonblank CHECK (reason ~ '\S');

-- 4. Full history of requests per absence (the partial unique index only covers open/approved ones).
CREATE INDEX justification_requests_record_idx ON justification_requests (attendance_record_id);
