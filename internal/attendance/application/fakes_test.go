package application

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/attendance/domain"
	"github.com/edutrak/backend/internal/attendance/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// fakeClock is advanced by tests; check-in times always come from it.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeTokens is deterministic: token n hashes to "hash(token-n)".
type fakeTokens struct{ n int }

func (f *fakeTokens) New() (string, string, error) {
	f.n++
	tok := "token-" + string(rune('0'+f.n))
	return tok, f.Hash(tok), nil
}
func (f *fakeTokens) Hash(t string) string { return "hash(" + t + ")" }

// fakeNetwork allows exactly the listed prefixes.
type fakeNetwork struct{ allowed []netip.Prefix }

func (n fakeNetwork) Allows(ip netip.Addr) bool {
	for _, p := range n.allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

type fakeRepo struct {
	sessions map[uuid.UUID]*domain.Session
	records  map[uuid.UUID]*domain.Record
	// enrollments by assignment, used by MarkAbsentees
	active map[uuid.UUID][]uuid.UUID

	lastScope  ports.ViewScope
	readCalls  int
	lockedWith []ports.Lock
}

func (f *fakeRepo) CreateSession(_ context.Context, s *domain.Session) error {
	for _, o := range f.sessions {
		if o.AssignmentID == s.AssignmentID && o.Status == domain.SessionOpen {
			return domain.ErrSessionAlreadyOpen
		}
	}
	c := *s
	f.sessions[s.ID] = &c
	return nil
}
func (f *fakeRepo) GetSession(_ context.Context, id uuid.UUID, lock ports.Lock) (*domain.Session, error) {
	f.lockedWith = append(f.lockedWith, lock)
	if s, ok := f.sessions[id]; ok {
		c := *s
		return &c, nil
	}
	return nil, domain.ErrSessionNotFound
}
func (f *fakeRepo) SetQR(_ context.Context, id uuid.UUID, hash string, exp time.Time) error {
	s := f.sessions[id]
	s.QRTokenHash, s.QRExpiresAt = hash, &exp
	return nil
}
func (f *fakeRepo) CloseSession(_ context.Context, id uuid.UUID, at time.Time) error {
	s := f.sessions[id]
	s.Status, s.ClosedAt, s.QRTokenHash, s.QRExpiresAt = domain.SessionClosed, &at, "", nil
	return nil
}
func (f *fakeRepo) MarkAbsentees(_ context.Context, sessionID, assignmentID, by uuid.UUID, at time.Time) (int, error) {
	n := 0
	for _, se := range f.active[assignmentID] {
		has := false
		for _, r := range f.records {
			if r.SessionID == sessionID && r.SubjectEnrollmentID == se {
				has = true
			}
		}
		if !has {
			id := uuid.New()
			f.records[id] = &domain.Record{ID: id, SessionID: sessionID, SubjectEnrollmentID: se, Status: domain.StatusAbsent,
				Method: domain.MethodManual, RecordedAt: at, RecordedBy: by}
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) CreateRecord(_ context.Context, r *domain.Record) error {
	for _, o := range f.records {
		if o.SessionID == r.SessionID && o.SubjectEnrollmentID == r.SubjectEnrollmentID {
			return domain.ErrAlreadyRegistered
		}
	}
	c := *r
	f.records[r.ID] = &c
	return nil
}
func (f *fakeRepo) GetRecord(_ context.Context, id uuid.UUID, _ bool) (*domain.Record, error) {
	if r, ok := f.records[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, domain.ErrRecordNotFound
}
func (f *fakeRepo) UpdateRecordStatus(_ context.Context, id uuid.UUID, st domain.RecordStatus) (*domain.Record, error) {
	r := f.records[id]
	r.Status = st
	c := *r
	return &c, nil
}
func (f *fakeRepo) GetAbsence(_ context.Context, id uuid.UUID, _ bool) (*domain.AbsenceRef, error) {
	if r, ok := f.records[id]; ok {
		return &domain.AbsenceRef{RecordID: id, Status: r.Status}, nil
	}
	return nil, domain.ErrRecordNotFound
}
func (f *fakeRepo) JustifyRecord(_ context.Context, id uuid.UUID) (bool, error) {
	if r, ok := f.records[id]; ok && r.Status == domain.StatusAbsent {
		r.Status = domain.StatusJustified
		return true, nil
	}
	return false, nil
}
func (f *fakeRepo) SessionDetail(context.Context, uuid.UUID) (*domain.SessionDetail, error) {
	f.readCalls++
	return &domain.SessionDetail{}, nil
}
func (f *fakeRepo) AssignmentOverview(context.Context, uuid.UUID) (*domain.AssignmentOverview, error) {
	f.readCalls++
	return &domain.AssignmentOverview{}, nil
}
func (f *fakeRepo) StudentAttendance(_ context.Context, _ uuid.UUID, scope ports.ViewScope) ([]domain.StudentAttendance, error) {
	f.readCalls++
	f.lastScope = scope
	return nil, nil
}

func (f *fakeRepo) countRecords(session uuid.UUID) int {
	n := 0
	for _, r := range f.records {
		if r.SessionID == session {
			n++
		}
	}
	return n
}

type fakeAcademic struct {
	assignments map[uuid.UUID]*domain.AssignmentRef
	schedules   map[uuid.UUID]uuid.UUID                // schedule -> assignment
	enrollments map[[2]uuid.UUID]*domain.EnrollmentRef // (assignment, student) -> enrollment
	byID        map[uuid.UUID]uuid.UUID                // enrollment -> assignment
	students    map[uuid.UUID]bool
	visible     bool
}

func (f *fakeAcademic) Assignment(_ context.Context, id uuid.UUID, _ bool) (*domain.AssignmentRef, error) {
	if a, ok := f.assignments[id]; ok {
		c := *a
		return &c, nil
	}
	return nil, domain.ErrAssignmentNotFound
}
func (f *fakeAcademic) ScheduleBelongs(_ context.Context, sch, a uuid.UUID) (bool, error) {
	return f.schedules[sch] == a, nil
}
func (f *fakeAcademic) StudentEnrollment(_ context.Context, a, student uuid.UUID) (*domain.EnrollmentRef, error) {
	if e, ok := f.enrollments[[2]uuid.UUID{a, student}]; ok {
		return e, nil
	}
	return nil, domain.ErrNotEnrolled
}
func (f *fakeAcademic) EnrollmentInAssignment(_ context.Context, enr, a uuid.UUID) (*domain.EnrollmentRef, error) {
	for _, e := range f.enrollments {
		if e.ID == enr && f.byID[enr] == a {
			return e, nil
		}
	}
	return nil, domain.ErrBodyEnrollmentNotFound
}
func (f *fakeAcademic) StudentExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.students[id], nil
}
func (f *fakeAcademic) StudentVisible(context.Context, uuid.UUID, ports.ViewScope) (bool, error) {
	return f.visible, nil
}

type fakeAudit struct {
	events []auditdomain.Event
	fail   error
}

func (f *fakeAudit) Record(_ context.Context, e auditdomain.Event) error {
	if f.fail != nil {
		return f.fail
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAudit) actions() []string {
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.Action
	}
	return out
}

// fakeTx emulates rollback by restoring sessions and records when fn fails.
type fakeTx struct{ repo *fakeRepo }

func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	sess := map[uuid.UUID]domain.Session{}
	for id, s := range t.repo.sessions {
		sess[id] = *s
	}
	recs := map[uuid.UUID]domain.Record{}
	for id, r := range t.repo.records {
		recs[id] = *r
	}
	if err := fn(ctx); err != nil {
		t.repo.sessions, t.repo.records = map[uuid.UUID]*domain.Session{}, map[uuid.UUID]*domain.Record{}
		for id, s := range sess {
			c := s
			t.repo.sessions[id] = &c
		}
		for id, r := range recs {
			c := r
			t.repo.records[id] = &c
		}
		return err
	}
	return nil
}

type env struct {
	t      *testing.T
	svc    *Service
	repo   *fakeRepo
	acad   *fakeAcademic
	audit  *fakeAudit
	clock  *fakeClock
	tokens *fakeTokens

	teacher, otherTeacher, tutor, otherTutor, director, admin iddomain.Principal
	s1, s2, outsider, withdrawn                               iddomain.Principal // students
	assignment                                                uuid.UUID
	se1, se2, seWithdrawn                                     uuid.UUID
}

func principal(roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.New(), SessionID: uuid.New(), Roles: roles}
}

var institutionalIP = netip.MustParseAddr("192.0.2.77") // documentation range: fictitious

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t,
		repo:   &fakeRepo{sessions: map[uuid.UUID]*domain.Session{}, records: map[uuid.UUID]*domain.Record{}, active: map[uuid.UUID][]uuid.UUID{}},
		audit:  &fakeAudit{},
		clock:  &fakeClock{now: time.Date(2026, 2, 2, 14, 0, 0, 0, time.UTC)},
		tokens: &fakeTokens{},
		acad: &fakeAcademic{assignments: map[uuid.UUID]*domain.AssignmentRef{}, schedules: map[uuid.UUID]uuid.UUID{},
			enrollments: map[[2]uuid.UUID]*domain.EnrollmentRef{}, byID: map[uuid.UUID]uuid.UUID{}, students: map[uuid.UUID]bool{}},
		teacher: principal(iddomain.RoleTeacher), otherTeacher: principal(iddomain.RoleTeacher),
		tutor: principal(iddomain.RoleTutor), otherTutor: principal(iddomain.RoleTutor),
		director: principal(iddomain.RoleDirector), admin: principal(iddomain.RoleAdmin),
		s1: principal(iddomain.RoleStudent), s2: principal(iddomain.RoleStudent),
		outsider: principal(iddomain.RoleStudent), withdrawn: principal(iddomain.RoleStudent),
		assignment: uuid.New(), se1: uuid.New(), se2: uuid.New(), seWithdrawn: uuid.New(),
	}
	e.acad.assignments[e.assignment] = &domain.AssignmentRef{ID: e.assignment, TeacherID: e.teacher.UserID,
		GroupTutorID: &e.tutor.UserID, TermID: uuid.New(), TermStatus: "active"}
	enrol := func(p iddomain.Principal, id uuid.UUID, status string) {
		e.acad.enrollments[[2]uuid.UUID{e.assignment, p.UserID}] = &domain.EnrollmentRef{ID: id, Status: status}
		e.acad.byID[id] = e.assignment
		e.acad.students[p.UserID] = true
		if status == "ACTIVE" {
			e.repo.active[e.assignment] = append(e.repo.active[e.assignment], id)
		}
	}
	enrol(e.s1, e.se1, "ACTIVE")
	enrol(e.s2, e.se2, "ACTIVE")
	enrol(e.withdrawn, e.seWithdrawn, "WITHDRAWN")
	e.acad.students[e.outsider.UserID] = true

	e.svc = NewService(Deps{
		Repo: e.repo, Academic: e.acad, Tokens: e.tokens,
		Network: fakeNetwork{allowed: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}},
		Clock:   e.clock, Audit: e.audit, Tx: fakeTx{e.repo}, NewID: uuid.New,
		QRTTL: 30 * time.Second, Location: time.UTC,
	})
	return e
}

// open starts a session as the owning teacher.
func (e *env) open() *domain.Session {
	e.t.Helper()
	s, err := e.svc.StartSession(context.Background(), e.teacher, StartSessionInput{AssignmentID: e.assignment})
	if err != nil {
		e.t.Fatalf("start session: %v", err)
	}
	return s
}

// openWithQR starts a session and rotates its QR, returning the token.
func (e *env) openWithQR() (*domain.Session, string) {
	e.t.Helper()
	s := e.open()
	qr, err := e.svc.RotateQR(context.Background(), e.teacher, s.ID)
	if err != nil {
		e.t.Fatalf("rotate: %v", err)
	}
	return s, qr.Token
}

func (e *env) checkIn(p iddomain.Principal, s *domain.Session, token string) (*domain.Record, error) {
	return e.svc.CheckIn(context.Background(), p, CheckInInput{SessionID: s.ID, Token: token, ClientIP: institutionalIP})
}
