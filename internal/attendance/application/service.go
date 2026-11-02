// Package application holds the attendance use cases. Every method receives
// the authenticated principal and decides on role + relationship to the
// resource: a student can only register their own attendance and a teacher only
// manages sessions of the assignments they teach. Identities are never read
// from request bodies.
package application

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/edutrak/backend/internal/attendance/domain"
	"github.com/edutrak/backend/internal/attendance/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type Deps struct {
	Repo     ports.Repository
	Academic ports.Academic
	Tokens   ports.QRTokens
	Network  ports.NetworkPolicy
	Clock    ports.Clock
	Audit    ports.AuditRecorder
	Tx       ports.Transactor
	NewID    func() uuid.UUID

	QRTTL                   time.Duration
	DefaultLateAfterMinutes int
	DefaultLatesPerAbsence  int
	// Location dates sessions: session_date is the calendar date in the institution timezone.
	Location *time.Location
}

type Service struct{ Deps }

func NewService(d Deps) *Service {
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.QRTTL <= 0 {
		d.QRTTL = 30 * time.Second
	}
	if d.DefaultLateAfterMinutes <= 0 {
		d.DefaultLateAfterMinutes = 15
	}
	if d.DefaultLatesPerAbsence <= 0 {
		d.DefaultLatesPerAbsence = 3
	}
	return &Service{d}
}

func (s *Service) audit(ctx context.Context, p iddomain.Principal, action, entityType string, entityID uuid.UUID, meta map[string]any) error {
	actor := p.UserID
	return s.Audit.Record(ctx, auditdomain.Event{
		ActorUserID: &actor, Action: action, EntityType: entityType, EntityID: entityID.String(), Metadata: meta,
	})
}

// requireOwningTeacher: only the teacher of the assignment manages its attendance.
func requireOwningTeacher(p iddomain.Principal, a *domain.AssignmentRef) error {
	if p.HasRole(iddomain.RoleTeacher) && a.TeacherID == p.UserID {
		return nil
	}
	return domain.ErrForbidden
}

// canViewAssignment: the teacher of the assignment, the tutor of its group, or a DIRECTOR.
func canViewAssignment(p iddomain.Principal, a *domain.AssignmentRef) bool {
	return p.HasRole(iddomain.RoleDirector) ||
		(p.HasRole(iddomain.RoleTeacher) && a.TeacherID == p.UserID) ||
		(p.HasRole(iddomain.RoleTutor) && a.GroupTutorID != nil && *a.GroupTutorID == p.UserID)
}

// writableSession loads (and locks) a session for a write by its owning teacher:
// the assignment must belong to them and its term must be open. Lock order is
// always session row first, then the term, so writers cannot deadlock.
func (s *Service) writableSession(ctx context.Context, p iddomain.Principal, sessionID uuid.UUID, lock ports.Lock) (*domain.Session, error) {
	sess, err := s.Repo.GetSession(ctx, sessionID, lock)
	if err != nil {
		return nil, err
	}
	a, err := s.Academic.Assignment(ctx, sess.AssignmentID, true)
	if err != nil {
		return nil, err
	}
	if err := requireOwningTeacher(p, a); err != nil {
		return nil, err
	}
	if err := a.RequireOpenTerm(); err != nil {
		return nil, err
	}
	return sess, nil
}

// --- A. start ---------------------------------------------------------------------------

type StartSessionInput struct {
	AssignmentID     uuid.UUID
	ScheduleID       *uuid.UUID
	LateAfterMinutes *int // optional; defaults to the configured value
	LatesPerAbsence  *int
}

// StartSession opens an attendance session for an assignment the teacher teaches.
// Only one session per assignment may be open at a time.
func (s *Service) StartSession(ctx context.Context, p iddomain.Principal, in StartSessionInput) (*domain.Session, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	lateAfter, lates := s.DefaultLateAfterMinutes, s.DefaultLatesPerAbsence
	if in.LateAfterMinutes != nil {
		lateAfter = *in.LateAfterMinutes
	}
	if in.LatesPerAbsence != nil {
		lates = *in.LatesPerAbsence
	}
	now := s.Clock.Now()
	local := now.In(s.Location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	sess, err := domain.NewSession(s.NewID(), in.AssignmentID, in.ScheduleID, p.UserID, now, date, lateAfter, lates)
	if err != nil {
		return nil, err
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		a, err := s.Academic.Assignment(ctx, in.AssignmentID, true)
		if err != nil {
			if errors.Is(err, domain.ErrAssignmentNotFound) {
				return domain.ErrBodyAssignmentNotFound
			}
			return err
		}
		if err := requireOwningTeacher(p, a); err != nil {
			return err
		}
		if err := a.RequireOpenTerm(); err != nil {
			return err
		}
		if in.ScheduleID != nil {
			ok, err := s.Academic.ScheduleBelongs(ctx, *in.ScheduleID, in.AssignmentID)
			if err != nil {
				return err
			}
			if !ok {
				return domain.ErrScheduleMismatch
			}
		}
		if err := s.Repo.CreateSession(ctx, sess); err != nil {
			return err
		}
		return s.audit(ctx, p, "attendance.session_started", "attendance_session", sess.ID, map[string]any{
			"teaching_assignment_id": sess.AssignmentID.String(), "session_date": date.Format("2006-01-02"),
			"late_after_minutes": sess.LateAfterMinutes, "lates_per_absence": sess.LatesPerAbsence,
		})
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// --- B. QR --------------------------------------------------------------------------------

type QRResult struct {
	SessionID uuid.UUID
	Token     string // returned once; only its hash is stored
	ExpiresAt time.Time
}

// RotateQR issues a new short-lived QR token, invalidating the previous one.
// It locks the session exclusively, so it serializes with check-ins and closing.
// Rotations are not audited individually: they happen every few seconds and
// carry no decision worth recording.
func (s *Service) RotateQR(ctx context.Context, p iddomain.Principal, sessionID uuid.UUID) (*QRResult, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	var out *QRResult
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		sess, err := s.writableSession(ctx, p, sessionID, ports.LockUpdate)
		if err != nil {
			return err
		}
		if err := sess.RequireOpen(); err != nil {
			return err
		}
		token, hash, err := s.Tokens.New()
		if err != nil {
			return err
		}
		expires := s.Clock.Now().Add(s.QRTTL)
		if err := s.Repo.SetQR(ctx, sess.ID, hash, expires); err != nil {
			return err
		}
		out = &QRResult{SessionID: sess.ID, Token: token, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- C. QR check-in -----------------------------------------------------------------------

type CheckInInput struct {
	SessionID uuid.UUID
	Token     string
	// ClientIP is the address derived from the real connection (or a trusted proxy
	// chain) by the HTTP adapter. The zero Addr means it could not be determined.
	ClientIP netip.Addr
}

// CheckIn registers the authenticated student's own attendance. The student is
// always p.UserID; there is no way to name another one.
func (s *Service) CheckIn(ctx context.Context, p iddomain.Principal, in CheckInInput) (*domain.Record, error) {
	if !p.HasRole(iddomain.RoleStudent) {
		return nil, domain.ErrForbidden
	}
	if in.Token == "" {
		return nil, domain.ErrTokenRequired
	}
	// The network is checked before touching any data, so outside the institution
	// nothing is learned about sessions.
	if !in.ClientIP.IsValid() || !s.Network.Allows(in.ClientIP) {
		return nil, domain.ErrNetworkNotAllowed
	}
	var rec *domain.Record
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		// FOR SHARE: check-ins run together, but never with a rotation or the closing.
		sess, err := s.Repo.GetSession(ctx, in.SessionID, ports.LockShare)
		if err != nil {
			return err
		}
		if err := sess.RequireOpen(); err != nil {
			return err
		}
		a, err := s.Academic.Assignment(ctx, sess.AssignmentID, true)
		if err != nil {
			return err
		}
		if err := a.RequireOpenTerm(); err != nil {
			return err
		}
		now := s.Clock.Now() // server time: the client never supplies it
		if err := sess.CheckQR(s.Tokens.Hash(in.Token), now); err != nil {
			return err
		}
		enr, err := s.Academic.StudentEnrollment(ctx, sess.AssignmentID, p.UserID)
		if err != nil {
			return err
		}
		if err := enr.RequireActive(); err != nil {
			return err
		}
		rec = &domain.Record{
			ID: s.NewID(), SessionID: sess.ID, SubjectEnrollmentID: enr.ID, Status: sess.Classify(now),
			Method: domain.MethodQR, RecordedAt: now, RecordedBy: p.UserID, IPAddress: in.ClientIP.String(),
		}
		if err := s.Repo.CreateRecord(ctx, rec); err != nil {
			return err
		}
		return s.audit(ctx, p, "attendance.recorded", "attendance_record", rec.ID, map[string]any{
			"session_id": sess.ID.String(), "subject_enrollment_id": enr.ID.String(),
			"status": string(rec.Status), "method": string(rec.Method),
		})
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// --- D. manual ----------------------------------------------------------------------------

type ManualInput struct {
	SessionID           uuid.UUID
	SubjectEnrollmentID uuid.UUID
	Status              string
}

// RegisterManually lets the responsible teacher register PRESENT, LATE or ABSENT
// for a student of the session's assignment. It does not depend on the QR or
// the network. JUSTIFIED is never accepted here.
func (s *Service) RegisterManually(ctx context.Context, p iddomain.Principal, in ManualInput) (*domain.Record, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	status, err := domain.ParseWritableStatus(in.Status)
	if err != nil {
		return nil, err
	}
	var rec *domain.Record
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		sess, err := s.writableSession(ctx, p, in.SessionID, ports.LockShare)
		if err != nil {
			return err
		}
		if err := sess.RequireOpen(); err != nil {
			return err
		}
		enr, err := s.Academic.EnrollmentInAssignment(ctx, in.SubjectEnrollmentID, sess.AssignmentID)
		if err != nil {
			return err
		}
		if err := enr.RequireActive(); err != nil {
			return err
		}
		rec = &domain.Record{
			ID: s.NewID(), SessionID: sess.ID, SubjectEnrollmentID: enr.ID, Status: status,
			Method: domain.MethodManual, RecordedAt: s.Clock.Now(), RecordedBy: p.UserID,
		}
		if err := s.Repo.CreateRecord(ctx, rec); err != nil {
			return err
		}
		return s.audit(ctx, p, "attendance.recorded", "attendance_record", rec.ID, map[string]any{
			"session_id": sess.ID.String(), "subject_enrollment_id": enr.ID.String(),
			"status": string(status), "method": string(rec.Method),
		})
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// --- E. correction ------------------------------------------------------------------------

// CorrectRecord changes the status of an existing record to PRESENT, LATE or ABSENT.
//
// Policy (not formally defined by the institution, so deliberately narrow): only the
// responsible teacher, while the term is open, on open OR closed sessions (closing
// creates ABSENT records, so forgetting someone must stay fixable); never to or from
// JUSTIFIED, which belongs to the justification flow. Every change is audited with
// the previous and new status; the record keeps its original method and recorder.
func (s *Service) CorrectRecord(ctx context.Context, p iddomain.Principal, recordID uuid.UUID, rawStatus string) (*domain.Record, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	status, err := domain.ParseWritableStatus(rawStatus)
	if err != nil {
		return nil, err
	}
	var out *domain.Record
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		peek, err := s.Repo.GetRecord(ctx, recordID, false)
		if err != nil {
			return err
		}
		sess, err := s.writableSession(ctx, p, peek.SessionID, ports.LockShare) // session first, then the record
		if err != nil {
			return err
		}
		rec, err := s.Repo.GetRecord(ctx, recordID, true)
		if err != nil {
			return err
		}
		if rec.Status == domain.StatusJustified {
			return domain.ErrRecordJustified
		}
		if rec.Status == status {
			out = rec
			return nil
		}
		updated, err := s.Repo.UpdateRecordStatus(ctx, recordID, status)
		if err != nil {
			return err
		}
		out = updated
		return s.audit(ctx, p, "attendance.corrected", "attendance_record", recordID, map[string]any{
			"session_id": sess.ID.String(), "subject_enrollment_id": rec.SubjectEnrollmentID.String(),
			"from": string(rec.Status), "to": string(status), "session_status": string(sess.Status),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- F. close -----------------------------------------------------------------------------

type CloseResult struct {
	Session         *domain.Session
	AbsencesCreated int
}

// CloseSession ends the session atomically: it takes the session exclusively (so no
// check-in can interleave), clears the QR, records ABSENT for every active enrolled
// student without a record, and marks the session CLOSED.
func (s *Service) CloseSession(ctx context.Context, p iddomain.Principal, sessionID uuid.UUID) (*CloseResult, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	var out *CloseResult
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		sess, err := s.writableSession(ctx, p, sessionID, ports.LockUpdate)
		if err != nil {
			return err
		}
		if err := sess.RequireOpen(); err != nil {
			return err
		}
		now := s.Clock.Now()
		created, err := s.Repo.MarkAbsentees(ctx, sess.ID, sess.AssignmentID, p.UserID, now)
		if err != nil {
			return err
		}
		if err := s.Repo.CloseSession(ctx, sess.ID, now); err != nil {
			return err
		}
		sess.Status, sess.ClosedAt, sess.QRTokenHash, sess.QRExpiresAt = domain.SessionClosed, &now, "", nil
		out = &CloseResult{Session: sess, AbsencesCreated: created}
		return s.audit(ctx, p, "attendance.session_closed", "attendance_session", sess.ID, map[string]any{
			"teaching_assignment_id": sess.AssignmentID.String(), "absences_created": created,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- G. reads -----------------------------------------------------------------------------

// GetSession returns a session with one row per enrolled student, to the
// teacher of its assignment, the tutor of its group, or a DIRECTOR.
func (s *Service) GetSession(ctx context.Context, p iddomain.Principal, sessionID uuid.UUID) (*domain.SessionDetail, error) {
	if !p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor) {
		return nil, domain.ErrForbidden
	}
	sess, err := s.Repo.GetSession(ctx, sessionID, ports.LockNone)
	if err != nil {
		return nil, err
	}
	a, err := s.Academic.Assignment(ctx, sess.AssignmentID, false)
	if err != nil {
		return nil, err
	}
	if !canViewAssignment(p, a) {
		return nil, domain.ErrForbidden
	}
	return s.Repo.SessionDetail(ctx, sessionID)
}

// GetAssignmentAttendance returns the sessions of an assignment and one summary
// per student (two queries, whatever the group size).
func (s *Service) GetAssignmentAttendance(ctx context.Context, p iddomain.Principal, assignmentID uuid.UUID) (*domain.AssignmentOverview, error) {
	if !p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor) {
		return nil, domain.ErrForbidden
	}
	a, err := s.Academic.Assignment(ctx, assignmentID, false)
	if err != nil {
		return nil, err
	}
	if !canViewAssignment(p, a) {
		return nil, domain.ErrForbidden
	}
	return s.Repo.AssignmentOverview(ctx, assignmentID)
}

// GetStudentAttendance returns every attempt of a student with their records.
//
//	STUDENT: only themselves · TEACHER: only attempts in their assignments ·
//	TUTOR: only attempts in groups they tutor · DIRECTOR: any student · ADMIN: never.
func (s *Service) GetStudentAttendance(ctx context.Context, p iddomain.Principal, studentID uuid.UUID) ([]domain.StudentAttendance, error) {
	director := p.HasRole(iddomain.RoleDirector)
	self := p.HasRole(iddomain.RoleStudent) && p.UserID == studentID
	scope := ports.ViewScope{All: director || self}
	if p.HasRole(iddomain.RoleTeacher) {
		id := p.UserID
		scope.TeacherID = &id
	}
	if p.HasRole(iddomain.RoleTutor) {
		id := p.UserID
		scope.TutorID = &id
	}
	if !scope.All && scope.TeacherID == nil && scope.TutorID == nil {
		return nil, domain.ErrForbidden
	}
	var visible bool
	var err error
	switch {
	case self:
		visible = true
	case director:
		visible, err = s.Academic.StudentExists(ctx, studentID)
		if err == nil && !visible {
			return nil, domain.ErrStudentNotFound
		}
	default:
		visible, err = s.Academic.StudentVisible(ctx, studentID, scope)
	}
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, domain.ErrForbidden
	}
	return s.Repo.StudentAttendance(ctx, studentID, scope)
}
