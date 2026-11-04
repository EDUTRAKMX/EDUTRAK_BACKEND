package application

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/google/uuid"
)

// --- fake attendance (the only door to attendance data) ---------------------------------------

type fakeAttendance struct {
	abs         map[uuid.UUID]*ports.Absence
	markFailure error
	locked      []uuid.UUID
}

func (f *fakeAttendance) Absence(_ context.Context, id uuid.UUID, lock bool) (*ports.Absence, error) {
	a, ok := f.abs[id]
	if !ok {
		return nil, domain.ErrRecordNotFound
	}
	if lock {
		f.locked = append(f.locked, id)
	}
	c := *a
	return &c, nil
}

func (f *fakeAttendance) MarkJustified(_ context.Context, id uuid.UUID) error {
	if f.markFailure != nil {
		return f.markFailure
	}
	a := f.abs[id]
	if a.Status != "ABSENT" {
		return domain.ErrAbsenceNotEligible
	}
	a.Status = "JUSTIFIED"
	return nil
}

// --- fake repository ----------------------------------------------------------------------------

type fakeRepo struct {
	att        *fakeAttendance
	requests   map[uuid.UUID]*domain.Justification
	evidence   map[uuid.UUID]*domain.Evidence
	reviews    []*domain.Review
	failCreate error
	lastScope  ports.ViewScope
	lastFilter *domain.Status
	order      []string // lock order: "record" / "request"
}

func (f *fakeRepo) Create(_ context.Context, j *domain.Justification) error {
	if f.failCreate != nil {
		return f.failCreate
	}
	for _, o := range f.requests {
		if o.AttendanceRecordID == j.AttendanceRecordID && (o.Status == domain.StatusPendingTutor || o.Status == domain.StatusPendingTeacher || o.Status == domain.StatusJustified) {
			return domain.ErrAlreadyRequested
		}
	}
	j.CreatedAt, j.UpdatedAt = time.Now(), time.Now()
	c := *j
	f.requests[j.ID] = &c
	return nil
}
func (f *fakeRepo) AddEvidence(_ context.Context, e *domain.Evidence) error {
	c := *e
	f.evidence[e.ID] = &c
	return nil
}
func (f *fakeRepo) HasActive(_ context.Context, rec uuid.UUID) (bool, error) {
	for _, o := range f.requests {
		if o.AttendanceRecordID == rec && !o.Status.Terminal() || o.AttendanceRecordID == rec && o.Status == domain.StatusJustified {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) caseOf(j *domain.Justification) *domain.Case {
	a := f.att.abs[j.AttendanceRecordID]
	n := 0
	for _, e := range f.evidence {
		if e.RequestID == j.ID {
			n++
		}
	}
	return &domain.Case{Justification: *j, EvidenceCount: n, Context: domain.Context{
		RecordStatus: a.Status, AssignmentID: a.AssignmentID, TeacherID: a.TeacherID, GroupTutorID: a.GroupTutorID, TermStatus: a.TermStatus,
	}}
}
func (f *fakeRepo) GetCase(_ context.Context, id uuid.UUID, lock bool) (*domain.Case, error) {
	j, ok := f.requests[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if lock {
		f.order = append(f.order, "request")
	}
	return f.caseOf(j), nil
}
func (f *fakeRepo) UpdateStatus(_ context.Context, id uuid.UUID, from, to domain.Status) (bool, error) {
	j := f.requests[id]
	if j.Status != from {
		return false, nil
	}
	j.Status = to
	return true, nil
}
func (f *fakeRepo) AddReview(_ context.Context, r *domain.Review) error {
	for _, o := range f.reviews {
		if o.RequestID == r.RequestID && o.Stage == r.Stage {
			return domain.ErrWrongStage
		}
	}
	c := *r
	f.reviews = append(f.reviews, &c)
	return nil
}
func (f *fakeRepo) Detail(ctx context.Context, id uuid.UUID) (*domain.Detail, error) {
	c, err := f.GetCase(ctx, id, false)
	if err != nil {
		return nil, err
	}
	d := &domain.Detail{Case: *c}
	for _, e := range f.evidence {
		if e.RequestID == id {
			d.Evidence = append(d.Evidence, *e)
		}
	}
	for _, r := range f.reviews {
		if r.RequestID == id {
			d.Reviews = append(d.Reviews, *r)
		}
	}
	return d, nil
}
func (f *fakeRepo) List(_ context.Context, s ports.ViewScope, st *domain.Status) ([]domain.Case, error) {
	f.lastScope, f.lastFilter = s, st
	return nil, nil
}
func (f *fakeRepo) Evidence(_ context.Context, reqID, evID uuid.UUID) (*domain.Evidence, error) {
	if e, ok := f.evidence[evID]; ok && e.RequestID == reqID {
		return e, nil
	}
	return nil, domain.ErrEvidenceNotFound
}

// --- fake storage -----------------------------------------------------------------------------------

type fakeStorage struct {
	objects   map[string][]byte
	deleted   []string
	putErr    error
	deleteErr error
}

func (f *fakeStorage) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	if f.putErr != nil {
		return 0, f.putErr
	}
	if _, exists := f.objects[key]; exists {
		return 0, errors.New("exists")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.objects[key] = b
	return int64(len(b)), nil
}
func (f *fakeStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	b, ok := f.objects[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.objects, key)
	return nil
}

type fakeAudit struct {
	events  []auditdomain.Event
	failOn  string // fail when this action is recorded ("" = never)
	failAll bool
}

func (f *fakeAudit) Record(_ context.Context, e auditdomain.Event) error {
	if f.failAll || (f.failOn != "" && e.Action == f.failOn) {
		return errors.New("audit down")
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

// fakeTx restores requests, evidence, reviews and attendance statuses when fn fails.
type fakeTx struct {
	repo *fakeRepo
	aud  *fakeAudit
}

func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	reqs := map[uuid.UUID]domain.Justification{}
	for id, j := range t.repo.requests {
		reqs[id] = *j
	}
	evs := map[uuid.UUID]domain.Evidence{}
	for id, e := range t.repo.evidence {
		evs[id] = *e
	}
	nrev, nevents := len(t.repo.reviews), len(t.aud.events)
	att := map[uuid.UUID]string{}
	for id, a := range t.repo.att.abs {
		att[id] = a.Status
	}
	if err := fn(ctx); err != nil {
		t.repo.requests, t.repo.evidence = map[uuid.UUID]*domain.Justification{}, map[uuid.UUID]*domain.Evidence{}
		for id, j := range reqs {
			c := j
			t.repo.requests[id] = &c
		}
		for id, e := range evs {
			c := e
			t.repo.evidence[id] = &c
		}
		t.repo.reviews = t.repo.reviews[:nrev]
		t.aud.events = t.aud.events[:nevents]
		for id, st := range att {
			t.repo.att.abs[id].Status = st
		}
		return err
	}
	return nil
}

// --- test environment -------------------------------------------------------------------------------

type env struct {
	t     *testing.T
	svc   *Service
	repo  *fakeRepo
	att   *fakeAttendance
	store *fakeStorage
	audit *fakeAudit
	logs  *strings.Builder

	student, otherStudent, tutor, otherTutor, teacher, otherTeacher, director, admin iddomain.Principal
	absent, present, late, other                                                     uuid.UUID // attendance record ids
}

func principal(roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.New(), SessionID: uuid.New(), Roles: roles}
}

var (
	pdfBytes = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 100)...)
	jpgBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("x"), 100)...)
	pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte("x"), 100)...)
)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, logs: &strings.Builder{},
		student: principal(iddomain.RoleStudent), otherStudent: principal(iddomain.RoleStudent),
		tutor: principal(iddomain.RoleTutor), otherTutor: principal(iddomain.RoleTutor),
		teacher: principal(iddomain.RoleTeacher), otherTeacher: principal(iddomain.RoleTeacher),
		director: principal(iddomain.RoleDirector), admin: principal(iddomain.RoleAdmin),
		absent: uuid.New(), present: uuid.New(), late: uuid.New(), other: uuid.New(),
	}
	assignment := uuid.New()
	mk := func(student iddomain.Principal, status string) *ports.Absence {
		return &ports.Absence{Status: status, StudentID: student.UserID, EnrollmentStatus: "ACTIVE", AssignmentID: assignment,
			TeacherID: e.teacher.UserID, GroupTutorID: &e.tutor.UserID, TermStatus: "active"}
	}
	e.att = &fakeAttendance{abs: map[uuid.UUID]*ports.Absence{
		e.absent: mk(e.student, "ABSENT"), e.present: mk(e.student, "PRESENT"), e.late: mk(e.student, "LATE"), e.other: mk(e.otherStudent, "ABSENT"),
	}}
	for id, a := range e.att.abs {
		a.RecordID = id
	}
	e.repo = &fakeRepo{att: e.att, requests: map[uuid.UUID]*domain.Justification{}, evidence: map[uuid.UUID]*domain.Evidence{}}
	e.store = &fakeStorage{objects: map[string][]byte{}}
	e.audit = &fakeAudit{}
	e.svc = NewService(Deps{
		Repo: e.repo, Attendance: e.att, Storage: e.store, Audit: e.audit, Tx: fakeTx{e.repo, e.audit}, NewID: uuid.New,
		Log: slog.New(slog.NewTextHandler(e.logs, nil)), MaxEvidenceBytes: 1024,
	})
	return e
}

// files builds a FileSource.
type fileList struct{ items []*Upload }

func (f *fileList) Next() (*Upload, error) {
	if len(f.items) == 0 {
		return nil, io.EOF
	}
	u := f.items[0]
	f.items = f.items[1:]
	return u, nil
}

func upload(name string, content []byte) *Upload {
	return &Upload{Filename: name, Content: bytes.NewReader(content)}
}

func files(us ...*Upload) FileSource { return &fileList{items: us} }

func (e *env) request(p iddomain.Principal, record uuid.UUID, fs FileSource) (*domain.Detail, error) {
	return e.svc.RequestJustification(context.Background(), p, RequestInput{AttendanceRecordID: record, Reason: "Consulta médica", Files: fs})
}

// advance walks a fresh request through the tutor's approval and returns its id.
func (e *env) pendingTeacher() uuid.UUID {
	e.t.Helper()
	d, err := e.request(e.student, e.absent, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.DecideAsTutor(context.Background(), e.tutor, d.ID, DecisionInput{Decision: "APPROVED"}); err != nil {
		e.t.Fatal(err)
	}
	return d.ID
}
