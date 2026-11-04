package application

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/attendance/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

// --- A. start -----------------------------------------------------------------------------

func TestStartSession(t *testing.T) {
	e := newEnv(t)
	s, err := e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment})
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != domain.SessionOpen || s.OpenedBy != e.teacher.UserID || s.LateAfterMinutes != 15 || s.LatesPerAbsence != 3 || !s.OpenedAt.Equal(e.clock.now) {
		t.Fatalf("session = %+v", s)
	}
	if got := e.audit.actions(); len(got) != 1 || got[0] != "attendance.session_started" {
		t.Fatalf("audit = %v", got)
	}
	// a second one while the first is open is refused; after closing it is fine
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment})
	wantErr(t, err, domain.ErrSessionAlreadyOpen)
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment}); err != nil {
		t.Fatalf("a new session after closing must be allowed: %v", err)
	}
}

func TestStartSessionAuthorizationAndValidation(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]iddomain.Principal{
		"other teacher": e.otherTeacher, "tutor": e.tutor, "student": e.s1, "director": e.director, "admin": e.admin,
	} {
		if _, err := e.svc.StartSession(ctx, p, StartSessionInput{AssignmentID: e.assignment}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	if len(e.repo.sessions) != 0 {
		t.Fatal("unauthorized attempts opened a session")
	}
	_, err := e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: uuid.New()})
	wantErr(t, err, domain.ErrBodyAssignmentNotFound)

	zero, big := 0, 5000
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, LateAfterMinutes: &zero})
	wantErr(t, err, domain.ErrInvalidLateAfter)
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, LatesPerAbsence: &zero})
	wantErr(t, err, domain.ErrInvalidLatesPerAbs)
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, LateAfterMinutes: &big})
	wantErr(t, err, domain.ErrInvalidLateAfter)

	sched := uuid.New()
	e.acad.schedules[sched] = uuid.New() // belongs to another assignment
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, ScheduleID: &sched})
	wantErr(t, err, domain.ErrScheduleMismatch)
	e.acad.schedules[sched] = e.assignment
	if _, err := e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, ScheduleID: &sched}); err != nil {
		t.Fatalf("own schedule: %v", err)
	}
}

func TestStartSessionCustomConfigAndClosedTerm(t *testing.T) {
	e := newEnv(t)
	five, two := 5, 2
	s, err := e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment, LateAfterMinutes: &five, LatesPerAbsence: &two})
	if err != nil || s.LateAfterMinutes != 5 || s.LatesPerAbsence != 2 {
		t.Fatalf("%v %+v", err, s)
	}
	e.repo.sessions = map[uuid.UUID]*domain.Session{}
	e.acad.assignments[e.assignment].TermStatus = "closed"
	_, err = e.svc.StartSession(ctx, e.teacher, StartSessionInput{AssignmentID: e.assignment})
	wantErr(t, err, domain.ErrTermClosed)
}

func TestSessionDateUsesTheInstitutionTimezone(t *testing.T) {
	e := newEnv(t)
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Skip("tzdata not available")
	}
	e.svc.Location = loc
	e.clock.now = time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC) // 21:00 on 28 Feb in Mexico City (UTC-6)
	s := e.open()
	if got := s.Date.Format("2006-01-02"); got != "2026-02-28" {
		t.Fatalf("session date = %s, want the local date 2026-02-28", got)
	}
}

// --- B. QR --------------------------------------------------------------------------------

func TestRotateQRStoresOnlyTheHashAndInvalidatesTheOldOne(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	first, err := e.svc.RotateQR(ctx, e.teacher, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ExpiresAt.Equal(e.clock.now.Add(30 * time.Second)) {
		t.Fatalf("expires at %v", first.ExpiresAt)
	}
	stored := e.repo.sessions[s.ID].QRTokenHash
	if stored == first.Token || stored != e.tokens.Hash(first.Token) {
		t.Fatalf("stored %q, token %q: only the hash may be persisted", stored, first.Token)
	}
	second, err := e.svc.RotateQR(ctx, e.teacher, s.ID)
	if err != nil || second.Token == first.Token {
		t.Fatalf("rotation must change the token: %v", err)
	}
	if _, err := e.checkIn(e.s1, s, first.Token); !errors.Is(err, domain.ErrInvalidQR) {
		t.Fatalf("a rotated-away token must stop working: %v", err)
	}
	if _, err := e.checkIn(e.s1, s, second.Token); err != nil {
		t.Fatalf("the new token must work: %v", err)
	}
}

func TestRotateQRAuthorizationAndState(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "student": e.s1, "tutor": e.tutor, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.RotateQR(ctx, p, s.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if e.repo.sessions[s.ID].QRTokenHash != "" {
		t.Fatal("a forbidden caller rotated the QR")
	}
	_, err := e.svc.RotateQR(ctx, e.teacher, uuid.New())
	wantErr(t, err, domain.ErrSessionNotFound)
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RotateQR(ctx, e.teacher, s.ID)
	wantErr(t, err, domain.ErrSessionClosed)
	for _, a := range e.audit.actions() {
		if a == "attendance.qr_rotated" {
			t.Fatal("QR rotations are intentionally not audited one by one")
		}
	}
}

// --- C. check-in --------------------------------------------------------------------------

func TestCheckInSuccessPresentThenLate(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	e.clock.advance(10 * time.Second)
	rec, err := e.checkIn(e.s1, s, token)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != domain.StatusPresent || rec.Method != domain.MethodQR || rec.SubjectEnrollmentID != e.se1 ||
		rec.RecordedBy != e.s1.UserID || rec.IPAddress != institutionalIP.String() || !rec.RecordedAt.Equal(e.clock.now) {
		t.Fatalf("record = %+v", rec)
	}
	// 15 minutes after the opening the same QR flow yields LATE (a fresh token is needed meanwhile)
	e.clock.advance(15 * time.Minute)
	qr, err := e.svc.RotateQR(ctx, e.teacher, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	late, err := e.checkIn(e.s2, s, qr.Token)
	if err != nil || late.Status != domain.StatusLate {
		t.Fatalf("late check-in: %v %+v", err, late)
	}
	if got := e.audit.actions(); got[len(got)-1] != "attendance.recorded" {
		t.Fatalf("audit = %v", got)
	}
	for _, ev := range e.audit.events {
		for _, v := range ev.Metadata {
			if v == token || v == qr.Token {
				t.Fatal("audit metadata must never contain the QR token")
			}
		}
	}
}

func TestCheckInQRValidity(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	_, err := e.checkIn(e.s1, s, "wrong-token")
	wantErr(t, err, domain.ErrInvalidQR)
	_, err = e.checkIn(e.s1, s, "")
	wantErr(t, err, domain.ErrTokenRequired)
	// a screenshot taken earlier does not work forever
	e.clock.advance(30 * time.Second)
	_, err = e.checkIn(e.s1, s, token)
	wantErr(t, err, domain.ErrQRExpired)
	e.clock.advance(time.Hour)
	_, err = e.checkIn(e.s1, s, token)
	wantErr(t, err, domain.ErrQRExpired)
	if e.repo.countRecords(s.ID) != 0 {
		t.Fatal("rejected check-ins stored records")
	}
	// a session that never issued a QR accepts nothing
	other := newEnv(t)
	s2 := other.open()
	_, err = other.checkIn(other.s1, s2, "anything")
	wantErr(t, err, domain.ErrInvalidQR)
}

func TestCheckInNetworkPolicy(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	for name, ip := range map[string]netip.Addr{
		"outside address":            netip.MustParseAddr("203.0.113.9"),
		"private but not configured": netip.MustParseAddr("10.0.0.5"),
		"neighbour range":            netip.MustParseAddr("192.0.3.1"),
		"unknown address":            {},
	} {
		_, err := e.svc.CheckIn(ctx, e.s1, CheckInInput{SessionID: s.ID, Token: token, ClientIP: ip})
		if !errors.Is(err, domain.ErrNetworkNotAllowed) {
			t.Errorf("%s: err = %v, want network_not_allowed", name, err)
		}
	}
	if e.repo.countRecords(s.ID) != 0 {
		t.Fatal("an unauthorized network registered attendance")
	}
	// the network is checked BEFORE any data is read: nothing leaks about the session
	_, err := e.svc.CheckIn(ctx, e.s1, CheckInInput{SessionID: uuid.New(), Token: token, ClientIP: netip.MustParseAddr("203.0.113.9")})
	wantErr(t, err, domain.ErrNetworkNotAllowed)
	// the whole campus behind one NAT address: many students, same IP, all accepted
	for _, p := range []iddomain.Principal{e.s1, e.s2} {
		if _, err := e.svc.CheckIn(ctx, p, CheckInInput{SessionID: s.ID, Token: token, ClientIP: netip.MustParseAddr("192.0.2.1")}); err != nil {
			t.Fatalf("shared NAT address: %v", err)
		}
	}
}

func TestCheckInRolesAndEnrollment(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	for name, p := range map[string]iddomain.Principal{"teacher": e.teacher, "tutor": e.tutor, "director": e.director, "admin": e.admin} {
		if _, err := e.checkIn(p, s, token); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	_, err := e.checkIn(e.outsider, s, token)
	wantErr(t, err, domain.ErrNotEnrolled)
	_, err = e.checkIn(e.withdrawn, s, token)
	wantErr(t, err, domain.ErrEnrollmentInactive)
	_, err = e.checkIn(e.s1, &domain.Session{ID: uuid.New()}, token)
	wantErr(t, err, domain.ErrSessionNotFound)
	if e.repo.countRecords(s.ID) != 0 {
		t.Fatal("rejected check-ins stored records")
	}
}

func TestCheckInRegistersOnlyTheAuthenticatedStudent(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	rec, err := e.checkIn(e.s2, s, token)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SubjectEnrollmentID != e.se2 || rec.RecordedBy != e.s2.UserID {
		t.Fatalf("student 2 registered %+v, must be their own enrollment", rec)
	}
	if e.repo.countRecords(s.ID) != 1 {
		t.Fatal("exactly one record expected")
	}
}

func TestCheckInDuplicateAndClosedSessionAndClosedTerm(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	first, err := e.checkIn(e.s1, s, token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.checkIn(e.s1, s, token)
	wantErr(t, err, domain.ErrAlreadyRegistered)
	if e.repo.records[first.ID].Status != domain.StatusPresent || e.repo.countRecords(s.ID) != 1 {
		t.Fatal("the duplicate must not touch the original record")
	}

	e.acad.assignments[e.assignment].TermStatus = "closed"
	_, err = e.checkIn(e.s2, s, token)
	wantErr(t, err, domain.ErrTermClosed)
	e.acad.assignments[e.assignment].TermStatus = "active"

	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.checkIn(e.s2, s, token)
	wantErr(t, err, domain.ErrSessionClosed)
}

func TestCheckInLocksTheSessionInShareMode(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	e.repo.lockedWith = nil
	if _, err := e.checkIn(e.s1, s, token); err != nil {
		t.Fatal(err)
	}
	if len(e.repo.lockedWith) != 1 || e.repo.lockedWith[0] != 1 { // ports.LockShare
		t.Fatalf("locks = %v, check-in must take the session FOR SHARE so it serializes with rotation and closing", e.repo.lockedWith)
	}
}

func TestCheckInRollsBackWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	e.audit.fail = errors.New("audit down")
	if _, err := e.checkIn(e.s1, s, token); err == nil {
		t.Fatal("check-in must fail when its audit cannot be written")
	}
	if e.repo.countRecords(s.ID) != 0 {
		t.Fatal("the record must roll back with the audit failure")
	}
}

// --- D. manual ----------------------------------------------------------------------------

func TestRegisterManually(t *testing.T) {
	e := newEnv(t)
	s := e.open() // no QR issued, and the network policy is irrelevant here
	for i, st := range []string{"PRESENT", "LATE", "ABSENT"} {
		se := []uuid.UUID{e.se1, e.se2, e.seWithdrawn}[i]
		if st == "ABSENT" {
			e.acad.enrollments[[2]uuid.UUID{e.assignment, e.withdrawn.UserID}].Status = "ACTIVE"
		}
		rec, err := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: se, Status: st})
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if string(rec.Status) != st || rec.Method != domain.MethodManual || rec.RecordedBy != e.teacher.UserID || rec.IPAddress != "" {
			t.Fatalf("%s: %+v", st, rec)
		}
	}
}

func TestRegisterManuallyRules(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	m := func(se uuid.UUID, st string) ManualInput {
		return ManualInput{SessionID: s.ID, SubjectEnrollmentID: se, Status: st}
	}

	_, err := e.svc.RegisterManually(ctx, e.teacher, m(e.se1, "JUSTIFIED"))
	wantErr(t, err, domain.ErrStatusNotAllowed)
	_, err = e.svc.RegisterManually(ctx, e.teacher, m(e.se1, "whatever"))
	wantErr(t, err, domain.ErrStatusNotAllowed)
	_, err = e.svc.RegisterManually(ctx, e.teacher, m(uuid.New(), "PRESENT"))
	wantErr(t, err, domain.ErrBodyEnrollmentNotFound)
	_, err = e.svc.RegisterManually(ctx, e.teacher, m(e.seWithdrawn, "PRESENT"))
	wantErr(t, err, domain.ErrEnrollmentInactive)
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "student": e.s1, "tutor": e.tutor, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.RegisterManually(ctx, p, m(e.se1, "PRESENT")); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if e.repo.countRecords(s.ID) != 0 {
		t.Fatal("rejected requests stored records")
	}
	if _, err := e.svc.RegisterManually(ctx, e.teacher, m(e.se1, "PRESENT")); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RegisterManually(ctx, e.teacher, m(e.se1, "ABSENT"))
	wantErr(t, err, domain.ErrAlreadyRegistered)
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RegisterManually(ctx, e.teacher, m(e.se2, "PRESENT"))
	wantErr(t, err, domain.ErrSessionClosed)
}

func TestManualCannotTouchAnEnrollmentOfAnotherAssignment(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	foreign, foreignAssignment := uuid.New(), uuid.New()
	e.acad.assignments[foreignAssignment] = &domain.AssignmentRef{ID: foreignAssignment, TeacherID: e.otherTeacher.UserID, TermStatus: "active"}
	e.acad.enrollments[[2]uuid.UUID{foreignAssignment, uuid.New()}] = &domain.EnrollmentRef{ID: foreign, Status: "ACTIVE"}
	e.acad.byID[foreign] = foreignAssignment
	_, err := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: foreign, Status: "PRESENT"})
	wantErr(t, err, domain.ErrBodyEnrollmentNotFound)
}

// --- E. correction ------------------------------------------------------------------------

func TestCorrectRecord(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	rec, _ := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: e.se1, Status: "ABSENT"})
	out, err := e.svc.CorrectRecord(ctx, e.teacher, rec.ID, "LATE")
	if err != nil || out.Status != domain.StatusLate {
		t.Fatalf("%v %+v", err, out)
	}
	ev := e.audit.events[len(e.audit.events)-1]
	if ev.Action != "attendance.corrected" || ev.Metadata["from"] != "ABSENT" || ev.Metadata["to"] != "LATE" || *ev.ActorUserID != e.teacher.UserID {
		t.Fatalf("event = %+v", ev)
	}
	// unchanged value: no write, no audit noise
	n := len(e.audit.events)
	if _, err := e.svc.CorrectRecord(ctx, e.teacher, rec.ID, "LATE"); err != nil || len(e.audit.events) != n {
		t.Fatalf("no-op correction: %v, audit %d -> %d", err, n, len(e.audit.events))
	}
}

func TestCorrectionPolicy(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	rec, _ := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: e.se1, Status: "PRESENT"})

	_, err := e.svc.CorrectRecord(ctx, e.teacher, rec.ID, "JUSTIFIED")
	wantErr(t, err, domain.ErrStatusNotAllowed)
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "student": e.s1, "tutor": e.tutor, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.CorrectRecord(ctx, p, rec.ID, "ABSENT"); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if e.repo.records[rec.ID].Status != domain.StatusPresent {
		t.Fatal("a forbidden correction changed the record")
	}
	_, err = e.svc.CorrectRecord(ctx, e.teacher, uuid.New(), "ABSENT")
	wantErr(t, err, domain.ErrRecordNotFound)

	// a record that the justification flow already justified cannot be overridden here
	e.repo.records[rec.ID].Status = domain.StatusJustified
	_, err = e.svc.CorrectRecord(ctx, e.teacher, rec.ID, "PRESENT")
	wantErr(t, err, domain.ErrRecordJustified)
	e.repo.records[rec.ID].Status = domain.StatusPresent

	// after the session closes the responsible teacher can still fix mistakes, audited
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	var absent uuid.UUID
	for id, r := range e.repo.records {
		if r.SubjectEnrollmentID == e.se2 {
			absent = id
		}
	}
	out, err := e.svc.CorrectRecord(ctx, e.teacher, absent, "PRESENT")
	if err != nil || out.Status != domain.StatusPresent {
		t.Fatalf("post-close correction: %v %+v", err, out)
	}
	if last := e.audit.events[len(e.audit.events)-1]; last.Metadata["session_status"] != "CLOSED" {
		t.Fatalf("post-close correction must be audited as such: %+v", last.Metadata)
	}
	// ... but never once the academic term is closed
	e.acad.assignments[e.assignment].TermStatus = "closed"
	_, err = e.svc.CorrectRecord(ctx, e.teacher, absent, "ABSENT")
	wantErr(t, err, domain.ErrTermClosed)
}

// --- F. close -----------------------------------------------------------------------------

func TestCloseSessionMarksAbsenteesIdempotently(t *testing.T) {
	e := newEnv(t)
	s, token := e.openWithQR()
	if _, err := e.checkIn(e.s1, s, token); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(20 * time.Minute)
	res, err := e.svc.CloseSession(ctx, e.teacher, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// s2 (active, no record) becomes ABSENT; the withdrawn student is NOT touched; s1 keeps PRESENT
	if res.AbsencesCreated != 1 || res.Session.Status != domain.SessionClosed || res.Session.ClosedAt == nil {
		t.Fatalf("result = %+v", res)
	}
	byEnr := map[uuid.UUID]domain.RecordStatus{}
	for _, r := range e.repo.records {
		byEnr[r.SubjectEnrollmentID] = r.Status
	}
	if byEnr[e.se1] != domain.StatusPresent || byEnr[e.se2] != domain.StatusAbsent {
		t.Fatalf("records = %v", byEnr)
	}
	if _, has := byEnr[e.seWithdrawn]; has {
		t.Fatal("a withdrawn enrollment must not get an absence")
	}
	if stored := e.repo.sessions[s.ID]; stored.QRTokenHash != "" || stored.QRExpiresAt != nil || stored.Status != domain.SessionClosed {
		t.Fatalf("stored session = %+v, the QR must be gone", stored)
	}
	// closing again is a state conflict and creates nothing more
	_, err = e.svc.CloseSession(ctx, e.teacher, s.ID)
	wantErr(t, err, domain.ErrSessionClosed)
	if e.repo.countRecords(s.ID) != 2 {
		t.Fatalf("records = %d, want 2", e.repo.countRecords(s.ID))
	}
	// the old QR is dead and so is the endpoint
	_, err = e.checkIn(e.s2, s, token)
	wantErr(t, err, domain.ErrSessionClosed)
	if got := e.audit.actions(); got[len(got)-1] != "attendance.session_closed" {
		t.Fatalf("audit = %v", got)
	}
}

func TestCloseSessionNeverRewritesLates(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if _, err := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: e.se1, Status: "LATE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.repo.records {
		if r.SubjectEnrollmentID == e.se1 && r.Status != domain.StatusLate {
			t.Fatalf("LATE became %s: lates are an indicator, never rewritten", r.Status)
		}
	}
}

func TestCloseSessionAuthorizationStateAndRollback(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "student": e.s1, "tutor": e.tutor, "director": e.director, "admin": e.admin} {
		if _, err := e.svc.CloseSession(ctx, p, s.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, err := e.svc.CloseSession(ctx, e.teacher, uuid.New())
	wantErr(t, err, domain.ErrSessionNotFound)
	if e.repo.sessions[s.ID].Status != domain.SessionOpen || e.repo.countRecords(s.ID) != 0 {
		t.Fatal("rejected closes changed the session")
	}

	e.audit.fail = errors.New("audit down")
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err == nil {
		t.Fatal("close must fail when its audit cannot be written")
	}
	if e.repo.sessions[s.ID].Status != domain.SessionOpen || e.repo.countRecords(s.ID) != 0 {
		t.Fatal("close must roll back completely (status and generated absences)")
	}
	e.audit.fail = nil
	e.acad.assignments[e.assignment].TermStatus = "closed"
	_, err = e.svc.CloseSession(ctx, e.teacher, s.ID)
	wantErr(t, err, domain.ErrTermClosed)
}

func TestCloseTakesTheSessionExclusively(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	e.repo.lockedWith = nil
	if _, err := e.svc.CloseSession(ctx, e.teacher, s.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.repo.lockedWith) != 1 || e.repo.lockedWith[0] != 2 { // ports.LockUpdate
		t.Fatalf("locks = %v, closing must hold the session FOR UPDATE so no check-in interleaves", e.repo.lockedWith)
	}
}

// --- G. reads -----------------------------------------------------------------------------

func TestReadAuthorizationByRelationship(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	for name, p := range map[string]iddomain.Principal{"teacher": e.teacher, "tutor": e.tutor, "director": e.director} {
		if _, err := e.svc.GetSession(ctx, p, s.ID); err != nil {
			t.Errorf("%s GetSession: %v", name, err)
		}
		if _, err := e.svc.GetAssignmentAttendance(ctx, p, e.assignment); err != nil {
			t.Errorf("%s GetAssignmentAttendance: %v", name, err)
		}
	}
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "other tutor": e.otherTutor, "student": e.s1, "admin": e.admin} {
		if _, err := e.svc.GetSession(ctx, p, s.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s GetSession: %v", name, err)
		}
		if _, err := e.svc.GetAssignmentAttendance(ctx, p, e.assignment); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s GetAssignmentAttendance: %v", name, err)
		}
	}
	_, err := e.svc.GetSession(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrSessionNotFound)
	_, err = e.svc.GetAssignmentAttendance(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrAssignmentNotFound)
}

func TestStudentAttendanceScopes(t *testing.T) {
	e := newEnv(t)
	target := e.s1.UserID
	if _, err := e.svc.GetStudentAttendance(ctx, e.s1, target); err != nil || !e.repo.lastScope.All {
		t.Fatalf("self: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.GetStudentAttendance(ctx, e.s2, target); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other student: %v", err)
	}
	if _, err := e.svc.GetStudentAttendance(ctx, e.admin, target); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin: %v", err)
	}
	if _, err := e.svc.GetStudentAttendance(ctx, e.director, target); err != nil || !e.repo.lastScope.All {
		t.Fatalf("director: %v", err)
	}
	_, err := e.svc.GetStudentAttendance(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrStudentNotFound)

	e.acad.visible = true
	if _, err := e.svc.GetStudentAttendance(ctx, e.teacher, target); err != nil || e.repo.lastScope.All || e.repo.lastScope.TeacherID == nil || e.repo.lastScope.TutorID != nil {
		t.Fatalf("teacher scope: %v %+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.GetStudentAttendance(ctx, e.tutor, target); err != nil || e.repo.lastScope.All || e.repo.lastScope.TutorID == nil || e.repo.lastScope.TeacherID != nil {
		t.Fatalf("tutor scope: %v %+v", err, e.repo.lastScope)
	}
	e.acad.visible = false
	calls := e.repo.readCalls
	for name, p := range map[string]iddomain.Principal{"teacher": e.otherTeacher, "tutor": e.otherTutor} {
		if _, err := e.svc.GetStudentAttendance(ctx, p, target); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s without relation: %v", name, err)
		}
	}
	if e.repo.readCalls != calls {
		t.Fatal("forbidden callers must not reach the attendance query")
	}
}

// --- internal API used by the justification flow ---------------------------------------------------

func TestJustifyAbsenceOnlyFromAbsent(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	mk := func(se uuid.UUID, st string) uuid.UUID {
		r, err := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: se, Status: st})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	absent, late := mk(e.se1, "ABSENT"), mk(e.se2, "LATE")

	if err := e.svc.JustifyAbsence(ctx, late); !errors.Is(err, domain.ErrAbsenceNotEligible) {
		t.Fatalf("LATE: %v", err)
	}
	if e.repo.records[late].Status != domain.StatusLate {
		t.Fatal("a LATE record must stay LATE")
	}
	if err := e.svc.JustifyAbsence(ctx, absent); err != nil {
		t.Fatal(err)
	}
	if e.repo.records[absent].Status != domain.StatusJustified {
		t.Fatal("ABSENT must become JUSTIFIED")
	}
	if err := e.svc.JustifyAbsence(ctx, absent); !errors.Is(err, domain.ErrAbsenceNotEligible) {
		t.Fatalf("justifying twice: %v", err)
	}
	if err := e.svc.JustifyAbsence(ctx, uuid.New()); !errors.Is(err, domain.ErrAbsenceNotEligible) {
		t.Fatalf("unknown record: %v", err)
	}
	// the PUBLIC paths still refuse JUSTIFIED
	if _, err := e.svc.CorrectRecord(ctx, e.teacher, late, "JUSTIFIED"); !errors.Is(err, domain.ErrStatusNotAllowed) {
		t.Fatalf("PATCH to JUSTIFIED: %v", err)
	}
	if _, err := e.svc.RegisterManually(ctx, e.teacher, ManualInput{SessionID: s.ID, SubjectEnrollmentID: e.seWithdrawn, Status: "JUSTIFIED"}); !errors.Is(err, domain.ErrStatusNotAllowed) {
		t.Fatalf("manual JUSTIFIED: %v", err)
	}
	// a justified record can no longer be corrected through the public endpoint
	if _, err := e.svc.CorrectRecord(ctx, e.teacher, absent, "PRESENT"); !errors.Is(err, domain.ErrRecordJustified) {
		t.Fatalf("correcting a JUSTIFIED record: %v", err)
	}
}
