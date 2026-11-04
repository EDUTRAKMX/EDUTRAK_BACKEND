package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

func (e *env) noTrace(what string) {
	e.t.Helper()
	if len(e.repo.requests) != 0 || len(e.repo.evidence) != 0 || len(e.store.objects) != 0 || len(e.audit.events) != 0 {
		e.t.Fatalf("%s left traces: %d requests, %d evidence rows, %d stored files, %d audit rows",
			what, len(e.repo.requests), len(e.repo.evidence), len(e.store.objects), len(e.audit.events))
	}
}

// --- A. request ----------------------------------------------------------------------------------------

func TestStudentRequestsTheirOwnAbsence(t *testing.T) {
	e := newEnv(t)
	d, err := e.request(e.student, e.absent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.StatusPendingTutor || d.StudentID != e.student.UserID || d.AttendanceRecordID != e.absent || d.Reason != "Consulta médica" {
		t.Fatalf("request = %+v", d.Justification)
	}
	if got := e.audit.actions(); len(got) != 1 || got[0] != "justifications.requested" {
		t.Fatalf("audit = %v", got)
	}
	if len(e.att.locked) != 1 || e.att.locked[0] != e.absent {
		t.Fatal("the attendance record must be locked while the request is created")
	}
}

func TestRequestEligibility(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		p      iddomain.Principal
		record uuid.UUID
		want   error
	}{
		{"another student's absence", e.student, e.other, domain.ErrForbidden},
		{"PRESENT record", e.student, e.present, domain.ErrAbsenceNotEligible},
		{"LATE record", e.student, e.late, domain.ErrAbsenceNotEligible},
		{"unknown record", e.student, uuid.New(), domain.ErrRecordNotFound},
		{"teacher", e.teacher, e.absent, domain.ErrForbidden},
		{"tutor", e.tutor, e.absent, domain.ErrForbidden},
		{"director", e.director, e.absent, domain.ErrForbidden},
		{"admin", e.admin, e.absent, domain.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.request(tt.p, tt.record, nil)
			wantErr(t, err, tt.want)
		})
	}
	e.noTrace("rejected requests")

	e.att.abs[e.absent].Status = "JUSTIFIED"
	_, err := e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrAbsenceNotEligible)
	e.att.abs[e.absent].Status = "ABSENT"
	e.att.abs[e.absent].EnrollmentStatus = "WITHDRAWN"
	_, err = e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrEnrollmentInactive)
	e.att.abs[e.absent].EnrollmentStatus = "ACTIVE"
	e.att.abs[e.absent].TermStatus = "closed"
	_, err = e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrTermClosed)
	e.noTrace("rejected requests")
}

func TestReasonIsValidated(t *testing.T) {
	e := newEnv(t)
	for _, reason := range []string{"", "   ", strings.Repeat("a", domain.MaxReasonLen+1)} {
		if _, err := e.svc.RequestJustification(ctx, e.student, RequestInput{AttendanceRecordID: e.absent, Reason: reason}); err == nil {
			t.Errorf("reason of %d chars must be rejected", len(reason))
		}
	}
	e.noTrace("invalid reasons")
}

func TestDuplicateOpenRequestAndNewRequestAfterRejection(t *testing.T) {
	e := newEnv(t)
	first, err := e.request(e.student, e.absent, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrAlreadyRequested)
	if len(e.repo.requests) != 1 {
		t.Fatal("the duplicate must not create a second request")
	}
	// reject it; the rejection stays in the history and a NEW request becomes possible
	if _, err := e.svc.DecideAsTutor(ctx, e.tutor, first.ID, DecisionInput{Decision: "REJECTED", Comment: "Sin comprobante"}); err != nil {
		t.Fatal(err)
	}
	second, err := e.request(e.student, e.absent, nil)
	if err != nil || second.ID == first.ID {
		t.Fatalf("a new request after a rejection: %v", err)
	}
	if e.repo.requests[first.ID].Status != domain.StatusTutorRejected || len(e.repo.reviews) != 1 {
		t.Fatal("the rejected request and its review must be kept")
	}
	// once JUSTIFIED no new request is possible
	e.att.abs[e.absent].Status = "JUSTIFIED"
	_, err = e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrAbsenceNotEligible)
}

func TestEvidenceIsStoredPrivatelyWithGeneratedKeys(t *testing.T) {
	e := newEnv(t)
	d, err := e.request(e.student, e.absent, files(
		upload("constancia.pdf", pdfBytes), upload("receta.JPG", jpgBytes), upload("foto.png", pngBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Evidence) != 3 || d.EvidenceCount != 3 || len(e.store.objects) != 3 {
		t.Fatalf("evidence rows=%d stored=%d", len(d.Evidence), len(e.store.objects))
	}
	keyRe := regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{32}\.(pdf|jpg|png)$`)
	mimes := map[string]string{}
	for _, ev := range d.Evidence {
		if !keyRe.MatchString(ev.StorageKey) {
			t.Fatalf("storage key %q is not a generated key", ev.StorageKey)
		}
		if strings.Contains(ev.StorageKey, "constancia") || strings.Contains(ev.StorageKey, "receta") {
			t.Fatal("the client's file name must never reach the storage key")
		}
		mimes[ev.OriginalFilename] = ev.MimeType
		if !bytes.Equal(e.store.objects[ev.StorageKey][:5], func() []byte { b, _ := io.ReadAll(mustOpen(t, e, ev.StorageKey)); return b[:5] }()) {
			t.Fatal("stored bytes differ")
		}
	}
	if mimes["constancia.pdf"] != "application/pdf" || mimes["receta.JPG"] != "image/jpeg" || mimes["foto.png"] != "image/png" {
		t.Fatalf("mime types = %v", mimes)
	}
}

func mustOpen(t *testing.T, e *env, key string) io.ReadCloser {
	t.Helper()
	rc, err := e.store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func TestEvidenceValidationRejectsBeforeStoringAnything(t *testing.T) {
	e := newEnv(t)
	exe := append([]byte("MZ\x90\x00"), bytes.Repeat([]byte("x"), 50)...)
	tests := []struct {
		name string
		up   *Upload
		want error
	}{
		{"executable disguised as pdf", upload("factura.pdf", exe), domain.ErrUnsupportedEvidence},
		{"png content with pdf extension", upload("x.pdf", pngBytes), domain.ErrEvidenceMismatch},
		{"pdf content with exe extension", upload("x.exe", pdfBytes), domain.ErrUnsupportedEvidence},
		{"html", upload("x.png", []byte("<html><script>1</script></html>")), domain.ErrUnsupportedEvidence},
		{"empty file", upload("x.pdf", nil), domain.ErrEmptyEvidence},
		{"bad file name", upload("", pdfBytes), domain.ErrInvalidFilename},
		{"too large", upload("big.pdf", append([]byte("%PDF-"), bytes.Repeat([]byte("a"), 2000)...)), domain.ErrEvidenceTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.request(e.student, e.absent, files(upload("ok.pdf", pdfBytes), tt.up))
			wantErr(t, err, tt.want)
		})
	}
	// the valid first file of each attempt was staged and then removed again
	e.noTrace("invalid evidence")
	if len(e.store.deleted) < len(tests) {
		t.Fatalf("staged files removed: %d", len(e.store.deleted))
	}
}

func TestOversizedUploadIsCutOffWhileReading(t *testing.T) {
	e := newEnv(t) // limit 1024 bytes
	endless := &countingReader{}
	_, err := e.request(e.student, e.absent, files(&Upload{Filename: "huge.pdf", Content: io.MultiReader(strings.NewReader("%PDF-"), endless)}))
	wantErr(t, err, domain.ErrEvidenceTooLarge)
	if endless.n > 4096 {
		t.Fatalf("read %d bytes of an endless upload: the cap must stop it right after the limit", endless.n)
	}
	e.noTrace("oversized upload")
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	c.n += len(p)
	return len(p), nil
}

func TestEvidenceCountAndRequirement(t *testing.T) {
	e := newEnv(t)
	var many []*Upload
	for i := 0; i < domain.MaxEvidenceFiles+1; i++ {
		many = append(many, upload(fmt.Sprintf("f%d.pdf", i), pdfBytes))
	}
	_, err := e.request(e.student, e.absent, files(many...))
	wantErr(t, err, domain.ErrTooManyFiles)
	e.noTrace("too many files")

	e.svc.EvidenceRequired = true
	_, err = e.request(e.student, e.absent, nil)
	wantErr(t, err, domain.ErrEvidenceRequired)
	_, err = e.request(e.student, e.absent, files())
	wantErr(t, err, domain.ErrEvidenceRequired)
	e.noTrace("missing required evidence")
	if _, err := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes))); err != nil {
		t.Fatalf("with evidence: %v", err)
	}
}

func TestMaliciousFilenamesNeverBecomePaths(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"../../etc/passwd.pdf", `..\..\windows\win.pdf`, "/abs/path.pdf", "a/b/c.pdf"} {
		e = newEnv(t)
		d, err := e.request(e.student, e.absent, files(upload(name, pdfBytes)))
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		ev := d.Evidence[0]
		if strings.ContainsAny(ev.OriginalFilename, `/\`) {
			t.Errorf("%q displayed as %q", name, ev.OriginalFilename)
		}
		for key := range e.store.objects {
			if strings.Contains(key, "..") || strings.Contains(key, "passwd") || strings.Contains(key, "win") || strings.Count(key, "/") != 1 {
				t.Errorf("%q produced the storage key %q", name, key)
			}
		}
	}
}

func TestStorageFailureLeavesNothingBehind(t *testing.T) {
	e := newEnv(t)
	e.store.putErr = errors.New("disk full")
	if _, err := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes))); err == nil {
		t.Fatal("a storage failure must fail the request")
	}
	e.noTrace("storage failure")
}

func TestDatabaseFailureRemovesStagedFiles(t *testing.T) {
	e := newEnv(t)
	e.repo.failCreate = errors.New("db down")
	if _, err := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes), upload("b.png", pngBytes))); err == nil {
		t.Fatal("expected a failure")
	}
	e.noTrace("database failure")
	if len(e.store.deleted) != 2 {
		t.Fatalf("deleted = %v, both staged files must be compensated", e.store.deleted)
	}
}

func TestAuditFailureRollsBackRequestAndRemovesFiles(t *testing.T) {
	e := newEnv(t)
	e.audit.failAll = true
	if _, err := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes))); err == nil {
		t.Fatal("request must fail when its audit row cannot be written")
	}
	e.noTrace("audit failure")
}

func TestCleanupFailureIsLoggedNotFatal(t *testing.T) {
	e := newEnv(t)
	e.repo.failCreate = errors.New("db down")
	e.store.deleteErr = errors.New("cannot delete")
	_, err := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes)))
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("the caller must see the ORIGINAL error, got %v", err)
	}
	if !strings.Contains(e.logs.String(), "could not remove staged evidence") {
		t.Fatalf("a failed cleanup must be logged: %q", e.logs.String())
	}
	if strings.Contains(e.logs.String(), "%PDF") {
		t.Fatal("file bytes must never be logged")
	}
}

// --- B/C. decisions ------------------------------------------------------------------------------------

func decision(d, comment string) DecisionInput { return DecisionInput{Decision: d, Comment: comment} }

func TestTutorApprovesThenTeacherJustifies(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)

	afterTutor, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", ""))
	if err != nil || afterTutor.Status != domain.StatusPendingTeacher {
		t.Fatalf("tutor approval: %v %+v", err, afterTutor)
	}
	if e.att.abs[e.absent].Status != "ABSENT" {
		t.Fatal("a tutor approval must NOT change the attendance")
	}
	final, err := e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision("APPROVED", "Documento válido"))
	if err != nil || final.Status != domain.StatusJustified {
		t.Fatalf("teacher approval: %v %+v", err, final)
	}
	if e.att.abs[e.absent].Status != "JUSTIFIED" {
		t.Fatal("the final approval must turn the attendance record into JUSTIFIED")
	}
	if len(final.Reviews) != 2 || final.Reviews[0].Stage != domain.StageTutor || final.Reviews[1].Stage != domain.StageTeacher ||
		final.Reviews[0].ReviewerID != e.tutor.UserID || final.Reviews[1].ReviewerID != e.teacher.UserID || final.Reviews[1].Comment != "Documento válido" {
		t.Fatalf("history = %+v", final.Reviews)
	}
	want := []string{"justifications.requested", "justifications.tutor_approved", "justifications.teacher_approved", "attendance.justified"}
	got := e.audit.actions()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	if m := e.audit.events[2].Metadata; m["from"] != "PENDING_TEACHER" || m["to"] != "JUSTIFIED" || *e.audit.events[2].ActorUserID != e.teacher.UserID {
		t.Fatalf("transition audit = %+v", e.audit.events[2])
	}
}

func TestRejectionsEndTheRequestAndKeepTheAbsence(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	if _, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("REJECTED", "  ")); !errors.Is(err, domain.ErrCommentRequired) {
		t.Fatalf("a rejection needs a comment: %v", err)
	}
	r, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("REJECTED", "Sin sustento"))
	if err != nil || r.Status != domain.StatusTutorRejected || len(r.Reviews) != 1 || r.Reviews[0].Comment != "Sin sustento" {
		t.Fatalf("tutor rejection: %v %+v", err, r)
	}
	// the tutor's rejection is final: the teacher cannot revive it
	_, err = e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrFinalized)
	if e.att.abs[e.absent].Status != "ABSENT" {
		t.Fatal("attendance must stay ABSENT")
	}

	// teacher rejection
	e2 := newEnv(t)
	id := e2.pendingTeacher()
	r2, err := e2.svc.DecideAsTeacher(ctx, e2.teacher, id, decision("REJECTED", "No corresponde"))
	if err != nil || r2.Status != domain.StatusTeacherRejected {
		t.Fatalf("teacher rejection: %v %+v", err, r2)
	}
	if e2.att.abs[e2.absent].Status != "ABSENT" {
		t.Fatal("a teacher rejection must keep the attendance ABSENT")
	}
	if len(r2.Reviews) != 2 || r2.Reviews[0].Decision != domain.DecisionApproved {
		t.Fatal("the tutor's earlier approval must be preserved, never overwritten by the teacher's rejection")
	}
	if got := e2.audit.actions(); got[len(got)-1] != "justifications.teacher_rejected" {
		t.Fatalf("audit = %v", got)
	}
}

func TestSequenceIsEnforced(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	// the teacher cannot decide before the tutor, approving or rejecting
	for _, dec := range []string{"APPROVED", "REJECTED"} {
		_, err := e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision(dec, "x"))
		wantErr(t, err, domain.ErrWrongStage)
	}
	if e.att.abs[e.absent].Status != "ABSENT" || e.repo.requests[d.ID].Status != domain.StatusPendingTutor || len(e.repo.reviews) != 0 {
		t.Fatal("an out-of-sequence attempt changed something")
	}
	if _, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", "")); err != nil {
		t.Fatal(err)
	}
	// the tutor cannot decide twice, nor take over the teacher's decision
	_, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrWrongStage)
	_, err = e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("REJECTED", "cambié de opinión"))
	wantErr(t, err, domain.ErrWrongStage)
	if e.att.abs[e.absent].Status != "ABSENT" {
		t.Fatal("a tutor can never justify an absence")
	}
	// the final decision, once, then immutable
	if _, err := e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision("APPROVED", "")); err != nil {
		t.Fatal(err)
	}
	for _, dec := range []string{"APPROVED", "REJECTED"} {
		_, err = e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision(dec, "otra vez"))
		wantErr(t, err, domain.ErrFinalized)
		_, err = e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision(dec, "otra vez"))
		wantErr(t, err, domain.ErrFinalized)
	}
	if len(e.repo.reviews) != 2 || e.att.abs[e.absent].Status != "JUSTIFIED" {
		t.Fatalf("history=%d attendance=%s", len(e.repo.reviews), e.att.abs[e.absent].Status)
	}
}

func TestDecisionAuthorizationByRoleAndRelationship(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	// tutor stage
	for name, p := range map[string]iddomain.Principal{"other tutor": e.otherTutor, "teacher": e.teacher, "student": e.student, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.DecideAsTutor(ctx, p, d.ID, decision("APPROVED", "")); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("tutor stage as %s: %v", name, err)
		}
	}
	if e.repo.requests[d.ID].Status != domain.StatusPendingTutor {
		t.Fatal("forbidden callers changed the request")
	}
	id := e.pendingTeacher2(d.ID)
	// teacher stage
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "tutor": e.tutor, "student": e.student, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.DecideAsTeacher(ctx, p, id, decision("APPROVED", "")); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("teacher stage as %s: %v", name, err)
		}
	}
	if e.att.abs[e.absent].Status != "ABSENT" {
		t.Fatal("forbidden callers justified the absence")
	}
	// a stranger learns nothing about the state: 403 even on a finished request
	if _, err := e.svc.DecideAsTeacher(ctx, e.teacher, id, decision("APPROVED", "")); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.DecideAsTeacher(ctx, e.otherTeacher, id, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrForbidden)
	_, err = e.svc.DecideAsTutor(ctx, e.otherTutor, id, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrForbidden)
}

func (e *env) pendingTeacher2(id uuid.UUID) uuid.UUID {
	e.t.Helper()
	if _, err := e.svc.DecideAsTutor(ctx, e.tutor, id, decision("APPROVED", "")); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestAuthorizationFollowsTheCurrentRelationship(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	// the group gets a new tutor: the previous one loses the right immediately
	e.att.abs[e.absent].GroupTutorID = &e.otherTutor.UserID
	_, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrForbidden)
	if _, err := e.svc.DecideAsTutor(ctx, e.otherTutor, d.ID, decision("APPROVED", "")); err != nil {
		t.Fatalf("the new tutor must be able to decide: %v", err)
	}
	// the assignment gets a new teacher
	e.att.abs[e.absent].TeacherID = e.otherTeacher.UserID
	_, err = e.svc.DecideAsTeacher(ctx, e.teacher, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrForbidden)
	if _, err := e.svc.DecideAsTeacher(ctx, e.otherTeacher, d.ID, decision("APPROVED", "")); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionValidationAndLookup(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	_, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("MAYBE", ""))
	wantErr(t, err, domain.ErrInvalidDecision)
	_, err = e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("", ""))
	wantErr(t, err, domain.ErrInvalidDecision)
	_, err = e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", strings.Repeat("x", domain.MaxCommentLen+1)))
	wantErr(t, err, domain.ErrCommentTooLong)
	_, err = e.svc.DecideAsTutor(ctx, e.tutor, uuid.New(), decision("APPROVED", ""))
	wantErr(t, err, domain.ErrNotFound)
	if len(e.repo.reviews) != 0 {
		t.Fatal("invalid decisions stored reviews")
	}
}

func TestApprovalFailsSafelyWhenTheAbsenceChanged(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	// the teacher corrected the attendance to PRESENT before the tutor decided
	e.att.abs[e.absent].Status = "PRESENT"
	_, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrAbsenceNotEligible)
	if e.repo.requests[d.ID].Status != domain.StatusPendingTutor {
		t.Fatal("a failed approval must not move the request")
	}
	// ...and a request stuck on a corrected record can still be closed by rejecting it
	if r, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("REJECTED", "La asistencia fue corregida")); err != nil || r.Status != domain.StatusTutorRejected {
		t.Fatalf("rejecting must stay possible: %v", err)
	}

	// final approval after a correction: nothing is forced to JUSTIFIED, everything rolls back
	e2 := newEnv(t)
	id := e2.pendingTeacher()
	e2.att.abs[e2.absent].Status = "LATE"
	_, err = e2.svc.DecideAsTeacher(ctx, e2.teacher, id, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrAbsenceNotEligible)
	if e2.att.abs[e2.absent].Status != "LATE" || e2.repo.requests[id].Status != domain.StatusPendingTeacher || len(e2.repo.reviews) != 1 {
		t.Fatalf("attendance=%s request=%s reviews=%d", e2.att.abs[e2.absent].Status, e2.repo.requests[id].Status, len(e2.repo.reviews))
	}
}

func TestClosedTermFreezesDecisions(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	e.att.abs[e.absent].TermStatus = "closed"
	_, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", ""))
	wantErr(t, err, domain.ErrTermClosed)
	_, err = e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("REJECTED", "x"))
	wantErr(t, err, domain.ErrTermClosed)
}

func TestFinalApprovalIsAtomic(t *testing.T) {
	// a failing audit row, a failing attendance update: nothing may survive
	for name, arrange := range map[string]func(*env){
		"audit of the transition fails": func(e *env) { e.audit.failOn = "justifications.teacher_approved" },
		"audit of attendance.justified": func(e *env) { e.audit.failOn = "attendance.justified" },
		"attendance update fails":       func(e *env) { e.att.markFailure = errors.New("attendance down") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.pendingTeacher()
			before := len(e.audit.events)
			arrange(e)
			if _, err := e.svc.DecideAsTeacher(ctx, e.teacher, id, decision("APPROVED", "")); err == nil {
				t.Fatal("the decision must fail")
			}
			if e.repo.requests[id].Status != domain.StatusPendingTeacher {
				t.Fatalf("request status = %s", e.repo.requests[id].Status)
			}
			if e.att.abs[e.absent].Status != "ABSENT" {
				t.Fatalf("attendance = %s: never JUSTIFIED without a JUSTIFIED request", e.att.abs[e.absent].Status)
			}
			if len(e.repo.reviews) != 1 || len(e.audit.events) != before {
				t.Fatalf("reviews=%d audit=%d: the teacher's review and audit must roll back", len(e.repo.reviews), len(e.audit.events))
			}
		})
	}
}

func TestTutorDecisionRollsBackWithItsAudit(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, nil)
	e.audit.failAll = true
	if _, err := e.svc.DecideAsTutor(ctx, e.tutor, d.ID, decision("APPROVED", "")); err == nil {
		t.Fatal("expected a failure")
	}
	if e.repo.requests[d.ID].Status != domain.StatusPendingTutor || len(e.repo.reviews) != 0 {
		t.Fatal("the tutor's decision must roll back with the audit failure")
	}
}

// --- reads ------------------------------------------------------------------------------------------------

func TestReadAuthorizationByRelationship(t *testing.T) {
	e := newEnv(t)
	d, _ := e.request(e.student, e.absent, files(upload("a.pdf", pdfBytes)))
	ev := d.Evidence[0]

	for name, p := range map[string]iddomain.Principal{"student": e.student, "tutor": e.tutor, "teacher": e.teacher, "director": e.director} {
		if _, err := e.svc.Get(ctx, p, d.ID); err != nil {
			t.Errorf("%s Get: %v", name, err)
		}
		_, rc, err := e.svc.OpenEvidence(ctx, p, d.ID, ev.ID)
		if err != nil {
			t.Errorf("%s OpenEvidence: %v", name, err)
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(b, pdfBytes) {
			t.Errorf("%s got different bytes", name)
		}
	}
	for name, p := range map[string]iddomain.Principal{"other student": e.otherStudent, "other tutor": e.otherTutor, "other teacher": e.otherTeacher, "admin": e.admin} {
		if _, err := e.svc.Get(ctx, p, d.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s Get: %v", name, err)
		}
		if _, _, err := e.svc.OpenEvidence(ctx, p, d.ID, ev.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s OpenEvidence: %v", name, err)
		}
	}
	_, err := e.svc.Get(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrNotFound)
	_, _, err = e.svc.OpenEvidence(ctx, e.student, d.ID, uuid.New())
	wantErr(t, err, domain.ErrEvidenceNotFound)

	// evidence of ANOTHER request cannot be fetched through this one
	e.att.abs[e.other].GroupTutorID = &e.tutor.UserID
	d2, _ := e.request(e.otherStudent, e.other, files(upload("b.pdf", pdfBytes)))
	_, _, err = e.svc.OpenEvidence(ctx, e.student, d.ID, d2.Evidence[0].ID)
	wantErr(t, err, domain.ErrEvidenceNotFound)

	// losing the relationship takes the evidence away at once
	e.att.abs[e.absent].GroupTutorID = &e.otherTutor.UserID
	if _, _, err := e.svc.OpenEvidence(ctx, e.tutor, d.ID, ev.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("the former tutor must lose access: %v", err)
	}
	if _, _, err := e.svc.OpenEvidence(ctx, e.otherTutor, d.ID, ev.ID); err != nil {
		t.Fatalf("the new tutor gains access: %v", err)
	}
}

func TestListBuildsScopeFromThePrincipalOnly(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.List(ctx, e.student, ""); err != nil || e.repo.lastScope.StudentID == nil || *e.repo.lastScope.StudentID != e.student.UserID || e.repo.lastScope.All || e.repo.lastScope.TutorID != nil {
		t.Fatalf("student scope: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.List(ctx, e.tutor, "PENDING_TUTOR"); err != nil || e.repo.lastScope.TutorID == nil || e.repo.lastScope.StudentID != nil || e.repo.lastFilter == nil || *e.repo.lastFilter != domain.StatusPendingTutor {
		t.Fatalf("tutor scope: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.List(ctx, e.teacher, ""); err != nil || e.repo.lastScope.TeacherID == nil || e.repo.lastScope.All {
		t.Fatalf("teacher scope: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.List(ctx, e.director, ""); err != nil || !e.repo.lastScope.All {
		t.Fatalf("director scope: %v %+v", err, e.repo.lastScope)
	}
	both := iddomain.Principal{UserID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTeacher, iddomain.RoleTutor}}
	if _, err := e.svc.List(ctx, both, ""); err != nil || e.repo.lastScope.TeacherID == nil || e.repo.lastScope.TutorID == nil || e.repo.lastScope.All {
		t.Fatalf("union scope: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.List(ctx, e.admin, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin: %v", err)
	}
	if _, err := e.svc.List(ctx, e.student, "BOGUS"); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatalf("bad filter: %v", err)
	}
}
