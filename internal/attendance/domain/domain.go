// Package domain holds the attendance entities and rules. It knows nothing
// about HTTP, SQL or tokens beyond comparing token hashes.
package domain

import (
	"crypto/subtle"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }
func forbidden(code, msg string) *apperr.Error  { return apperr.New(apperr.KindForbidden, code, msg) }

// Errors returned by the attendance module.
var (
	ErrForbidden = forbidden("forbidden", "you do not have permission to perform this action")

	ErrSessionNotFound    = notFound("attendance_session_not_found", "attendance session not found")
	ErrRecordNotFound     = notFound("attendance_record_not_found", "attendance record not found")
	ErrAssignmentNotFound = notFound("teaching_assignment_not_found", "teaching assignment not found")
	ErrStudentNotFound    = notFound("student_not_found", "student not found")

	// Referenced from a request body: reported as 422, like in the other modules.
	ErrBodyAssignmentNotFound = validation("teaching_assignment_not_found", "teaching assignment not found")
	ErrBodyEnrollmentNotFound = validation("subject_enrollment_not_found", "subject enrollment not found in this teaching assignment")
	ErrScheduleMismatch       = validation("schedule_not_in_assignment", "the schedule does not belong to this teaching assignment")

	ErrInvalidLateAfter   = validation("invalid_late_after_minutes", "late_after_minutes must be a positive number of minutes")
	ErrInvalidLatesPerAbs = validation("invalid_lates_per_absence", "lates_per_absence must be a positive number")
	ErrStatusNotAllowed   = validation("status_not_allowed", "status must be PRESENT, LATE or ABSENT; JUSTIFIED is decided by the justification flow")
	ErrTokenRequired      = validation("qr_token_required", "the QR token is required")

	ErrSessionAlreadyOpen = conflict("session_already_open", "this teaching assignment already has an open attendance session")
	ErrSessionClosed      = conflict("session_closed", "the attendance session is closed")
	ErrAlreadyRegistered  = conflict("attendance_already_registered", "attendance is already registered for this student in this session")
	ErrTermClosed         = conflict("term_closed", "the academic term is closed")
	ErrEnrollmentInactive = conflict("subject_enrollment_inactive", "the subject enrollment is not active")
	ErrAbsenceNotEligible = conflict("absence_not_eligible", "the attendance record is not an ABSENT record that can be justified")
	ErrRecordJustified    = conflict("record_justified", "a justified record cannot be changed here; it belongs to the justification flow")

	ErrNetworkNotAllowed = forbidden("network_not_allowed", "attendance can only be registered from the institutional network")
	ErrNotEnrolled       = forbidden("not_enrolled", "you are not enrolled in this teaching assignment")
	ErrInvalidQR         = forbidden("invalid_qr_token", "the QR code is not valid")
	ErrQRExpired         = forbidden("qr_expired", "the QR code has expired; scan the current one")
)

type SessionStatus string

const (
	SessionOpen   SessionStatus = "OPEN"
	SessionClosed SessionStatus = "CLOSED"
)

type RecordStatus string

const (
	StatusPresent   RecordStatus = "PRESENT"
	StatusLate      RecordStatus = "LATE"
	StatusAbsent    RecordStatus = "ABSENT"
	StatusJustified RecordStatus = "JUSTIFIED"
)

type Method string

const (
	MethodQR     Method = "QR"
	MethodNFC    Method = "NFC" // reserved: no hardware integration yet
	MethodManual Method = "MANUAL"
)

// Sanity limits for the configurable lateness rule (the schema only requires > 0).
const (
	MaxLateAfterMinutes = 1440
	MaxLatesPerAbsence  = 100
)

// Session is one class meeting of a teaching assignment.
type Session struct {
	ID               uuid.UUID
	AssignmentID     uuid.UUID
	ScheduleID       *uuid.UUID
	Date             time.Time // calendar date in the institution timezone (UTC midnight)
	Status           SessionStatus
	OpenedBy         uuid.UUID
	OpenedAt         time.Time
	ClosedAt         *time.Time
	QRTokenHash      string // SHA-256 hex of the current QR token; empty when none
	QRExpiresAt      *time.Time
	LateAfterMinutes int
	LatesPerAbsence  int
}

// NewSession validates the lateness configuration and builds an OPEN session.
func NewSession(id, assignmentID uuid.UUID, scheduleID *uuid.UUID, openedBy uuid.UUID, now, date time.Time, lateAfter, latesPerAbsence int) (*Session, error) {
	if lateAfter < 1 || lateAfter > MaxLateAfterMinutes {
		return nil, ErrInvalidLateAfter
	}
	if latesPerAbsence < 1 || latesPerAbsence > MaxLatesPerAbsence {
		return nil, ErrInvalidLatesPerAbs
	}
	return &Session{
		ID: id, AssignmentID: assignmentID, ScheduleID: scheduleID, Date: date, Status: SessionOpen,
		OpenedBy: openedBy, OpenedAt: now, LateAfterMinutes: lateAfter, LatesPerAbsence: latesPerAbsence,
	}, nil
}

// RequireOpen fails with ErrSessionClosed once the session has been closed.
func (s *Session) RequireOpen() error {
	if s.Status != SessionOpen {
		return ErrSessionClosed
	}
	return nil
}

// Classify decides PRESENT or LATE from the SERVER time of the check-in. The
// reference is the moment the session was opened: a check-in strictly before
// opened_at + late_after_minutes is PRESENT; from that instant on it is LATE.
func (s *Session) Classify(now time.Time) RecordStatus {
	if now.Before(s.OpenedAt.Add(time.Duration(s.LateAfterMinutes) * time.Minute)) {
		return StatusPresent
	}
	return StatusLate
}

// CheckQR verifies a presented token hash against the session's current QR.
// The token must match (constant-time) and must not be expired; rotating or
// closing the session clears the stored hash so older tokens stop matching.
func (s *Session) CheckQR(presentedHash string, now time.Time) error {
	if s.Status != SessionOpen || s.QRTokenHash == "" || s.QRExpiresAt == nil {
		return ErrInvalidQR
	}
	if subtle.ConstantTimeCompare([]byte(s.QRTokenHash), []byte(presentedHash)) != 1 {
		return ErrInvalidQR
	}
	if !now.Before(*s.QRExpiresAt) {
		return ErrQRExpired
	}
	return nil
}

// ParseWritableStatus validates a status a teacher may set. JUSTIFIED is
// reserved for the justification flow; NFC/QR are not statuses.
func ParseWritableStatus(s string) (RecordStatus, error) {
	switch RecordStatus(s) {
	case StatusPresent, StatusLate, StatusAbsent:
		return RecordStatus(s), nil
	}
	return "", ErrStatusNotAllowed
}

// Record is the attendance of one subject enrollment (student attempt) in one session.
type Record struct {
	ID                  uuid.UUID
	SessionID           uuid.UUID
	SubjectEnrollmentID uuid.UUID
	Status              RecordStatus
	Method              Method
	RecordedAt          time.Time
	RecordedBy          uuid.UUID
	IPAddress           string // real client address for QR check-ins; empty otherwise
}

// Counts tallies records by status.
type Counts struct{ Present, Late, Absent, Justified int }

func (c Counts) Total() int { return c.Present + c.Late + c.Absent + c.Justified }

// AbsencesFromLates is how many absences the lates add up to (N lates = 1
// absence, integer division). It is an indicator only: LATE records are never
// rewritten as ABSENT.
func (c Counts) AbsencesFromLates(latesPerAbsence int) int {
	if latesPerAbsence < 1 {
		return 0
	}
	return c.Late / latesPerAbsence
}

// EffectiveAbsences = recorded absences + absences equivalent to the lates.
// Justified records count as neither.
func (c Counts) EffectiveAbsences(latesPerAbsence int) int {
	return c.Absent + c.AbsencesFromLates(latesPerAbsence)
}

// --- read models ------------------------------------------------------------------

// AssignmentRef is what attendance needs to know about a teaching assignment.
type AssignmentRef struct {
	ID           uuid.UUID
	TeacherID    uuid.UUID
	GroupTutorID *uuid.UUID
	TermID       uuid.UUID
	TermStatus   string
}

// RequireOpenTerm fails when the assignment's term is closed.
func (a AssignmentRef) RequireOpenTerm() error {
	if a.TermStatus == "closed" {
		return ErrTermClosed
	}
	return nil
}

// AbsenceRef describes an attendance record together with the academic context the
// justification flow needs to authorize and validate a request. It is internal: it is
// never serialized by this module's HTTP adapter.
type AbsenceRef struct {
	RecordID            uuid.UUID
	Status              RecordStatus
	SessionID           uuid.UUID
	SessionDate         time.Time
	SubjectEnrollmentID uuid.UUID
	EnrollmentStatus    string
	StudentID           uuid.UUID
	AssignmentID        uuid.UUID
	TeacherID           uuid.UUID
	GroupTutorID        *uuid.UUID
	TermStatus          string
}

// EnrollmentRef is a subject enrollment as attendance sees it.
type EnrollmentRef struct {
	ID     uuid.UUID
	Status string // ACTIVE / WITHDRAWN
}

func (e EnrollmentRef) RequireActive() error {
	if e.Status != "ACTIVE" {
		return ErrEnrollmentInactive
	}
	return nil
}

type Person struct {
	ID                  uuid.UUID
	InstitutionalNumber string
	FirstName           string
	LastName            string
}

// SessionRow is one student of a session; Record is nil while nothing is registered.
type SessionRow struct {
	SubjectEnrollmentID uuid.UUID
	Student             Person
	Record              *Record
}

type SessionDetail struct {
	Session Session
	Rows    []SessionRow
	Counts  Counts
}

type SessionSummary struct {
	Session Session
	Counts  Counts
}

type StudentSummary struct {
	SubjectEnrollmentID uuid.UUID
	Student             Person
	Status              string
	Counts              Counts
}

// AssignmentOverview lists the sessions of an assignment and one summary per student.
// LatesPerAbsence comes from the most recently opened session (each session keeps its own copy).
type AssignmentOverview struct {
	AssignmentID    uuid.UUID
	Sessions        []SessionSummary
	Students        []StudentSummary
	LatesPerAbsence int
}

type StudentRecord struct {
	RecordID    uuid.UUID
	SessionID   uuid.UUID
	SessionDate time.Time
	Status      RecordStatus
	Method      Method
	RecordedAt  time.Time
}

// StudentAttendance is one attempt (subject enrollment) of a student with its records.
type StudentAttendance struct {
	SubjectEnrollmentID uuid.UUID
	AssignmentID        uuid.UUID
	AttemptNumber       int16
	TermID              uuid.UUID
	TermName            string
	GroupCode           string
	SubjectID           uuid.UUID
	SubjectCode         string
	SubjectName         string
	LatesPerAbsence     int
	Counts              Counts
	Records             []StudentRecord
}
