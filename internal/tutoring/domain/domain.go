// Package domain holds the tutoring entities and rules. It knows nothing about HTTP, SQL
// or JSON. A follow-up is an immutable historical record: a new intervention is a new row.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	attendancedomain "github.com/edutrak/backend/internal/attendance/domain"
	gradesdomain "github.com/edutrak/backend/internal/grades/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }

const (
	MaxNoteLength = 4000
	DateLayout    = "2006-01-02"
)

var (
	ErrForbidden = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")

	ErrFollowUpNotFound = notFound("followup_not_found", "follow-up not found")
	ErrStudentNotFound  = notFound("student_not_found", "student not found")
	ErrContextNotFound  = notFound("enrollment_not_found", "the student has no enrollment in that group")

	// Referenced from a request body: reported as 422, like in the other modules.
	ErrBodyStudentNotFound = validation("student_not_found", "student not found")
	ErrBodyGroupNotFound   = validation("group_not_found", "group not found")
	ErrNotEnrolled         = validation("student_not_enrolled", "the student is not enrolled in that group")

	ErrInvalidStudentID = validation("invalid_student_id", "student_id must be a valid UUID")
	ErrInvalidGroupID   = validation("invalid_group_id", "group_id must be a valid UUID")
	ErrNoteRequired     = validation("note_required", "the note is required")
	ErrNoteTooLong      = validation("note_too_long", "the note is too long")
	ErrInvalidDate      = validation("invalid_followup_date", "followup_date must be a valid date (YYYY-MM-DD)")
	ErrDateInFuture     = validation("followup_date_in_future", "the follow-up date cannot be in the future")
	ErrDateBeforeTerm   = validation("followup_date_before_term", "the follow-up date cannot precede the start of the academic term")
	ErrInvalidFilter    = validation("invalid_filter", "invalid filter value")
	ErrInvalidPaging    = validation("invalid_paging", "limit must be 1-200 and offset >= 0")

	ErrTermClosed         = conflict("term_closed", "the academic term is closed")
	ErrEnrollmentInactive = conflict("enrollment_inactive", "the student's enrollment in that group is not active")
)

// NewNote trims the note and enforces 1..MaxNoteLength characters. The content is free
// text written by the tutor; it must stay about academic accompaniment (see README).
func NewNote(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" {
		return "", ErrNoteRequired
	}
	if utf8.RuneCountInString(n) > MaxNoteLength {
		return "", ErrNoteTooLong
	}
	return n, nil
}

// ParseDate reads YYYY-MM-DD.
func ParseDate(s string) (time.Time, error) {
	d, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, ErrInvalidDate
	}
	return d, nil
}

// CheckFollowUpDate: the date of a follow-up lies between the start of the term and
// today (calendar date in the institution timezone). today and termStart are dates.
func CheckFollowUpDate(d, today, termStart time.Time) error {
	if d.After(today) {
		return ErrDateInFuture
	}
	if d.Before(termStart) {
		return ErrDateBeforeTerm
	}
	return nil
}

// FollowUp is one historical tutoring record.
type FollowUp struct {
	ID        uuid.UUID
	StudentID uuid.UUID
	TutorID   uuid.UUID // the author, kept even when the group later changes tutor
	GroupID   uuid.UUID
	Date      time.Time
	Note      string
	CreatedAt time.Time
}

// New validates and builds a follow-up. The tutor comes from the authenticated
// principal, never from the request.
func New(id, studentID, tutorID, groupID uuid.UUID, date time.Time, note string) (*FollowUp, error) {
	n, err := NewNote(note)
	if err != nil {
		return nil, err
	}
	return &FollowUp{ID: id, StudentID: studentID, TutorID: tutorID, GroupID: groupID, Date: date, Note: n}, nil
}

// Enrollment is the academic context of a student in one group, as tutoring needs it.
type Enrollment struct {
	StudentID          uuid.UUID
	StudentNumber      string
	StudentFirst       string
	StudentLast        string
	GroupID            uuid.UUID
	GroupCode          string
	Level              int
	GroupTutorID       *uuid.UUID
	CareerCode         string
	CareerName         string
	TermID             uuid.UUID
	TermName           string
	TermStatus         string
	TermStart, TermEnd time.Time
	EnrollmentStatus   string
	FollowUpCount      int
	LastFollowUpDate   *time.Time
}

func (e Enrollment) Active() bool { return e.EnrollmentStatus == "ACTIVE" }

// RequireWritable: follow-ups can only be added for an ACTIVE enrollment in an unclosed term.
func (e Enrollment) RequireWritable() error {
	if e.TermStatus == "closed" {
		return ErrTermClosed
	}
	if !e.Active() {
		return ErrEnrollmentInactive
	}
	return nil
}

// Entry is a follow-up with the names needed to display it.
type Entry struct {
	FollowUp
	StudentNumber string
	StudentFirst  string
	StudentLast   string
	TutorFirst    string
	TutorLast     string
	GroupCode     string
	GroupTutorID  *uuid.UUID // CURRENT tutor of the group (authorization), not the author
	TermID        uuid.UUID
	TermName      string
}

// --- academic overview (read model, built from Academic, Grades, Attendance, Justifications) -----

type UnitGrade struct {
	UnitID     uuid.UUID
	UnitNumber int
	UnitName   string
	Hundredths *int32 // nil = not captured yet (never 0.00)
}

// Score and Category reuse the Grades rules (no thresholds are duplicated here). Both are
// empty for a unit that has not been graded: pending is never 0.00.
func (u UnitGrade) Score() (string, string, bool) {
	if u.Hundredths == nil {
		return "", "", false
	}
	sc, err := gradesdomain.NewScoreFromHundredths(int64(*u.Hundredths))
	if err != nil {
		return "", "", false
	}
	return sc.String(), string(sc.Category()), true
}

type Attendance struct {
	Present, Late, Absent, Justified int
	LatesPerAbsence                  *int // from the most recently opened session; nil without sessions
}

// Indicators apply the Attendance rules (N lates = 1 absence, an indicator only: lates are
// never rewritten as absences). Without sessions there is no configured equivalence and both
// derived figures are 0.
func (a Attendance) Indicators() (total, absencesFromLates, effectiveAbsences int) {
	c := attendancedomain.Counts{Present: a.Present, Late: a.Late, Absent: a.Absent, Justified: a.Justified}
	if a.LatesPerAbsence == nil {
		return c.Total(), 0, c.Absent
	}
	return c.Total(), c.AbsencesFromLates(*a.LatesPerAbsence), c.EffectiveAbsences(*a.LatesPerAbsence)
}

type JustificationCounts struct{ Pending, Approved, Rejected int }

type SubjectOverview struct {
	SubjectEnrollmentID uuid.UUID
	AssignmentID        uuid.UUID
	SubjectCode         string
	SubjectName         string
	TeacherFirst        string
	TeacherLast         string
	AttemptNumber       int
	Status              string
	Units               []UnitGrade
	Attendance          Attendance
	Justifications      JustificationCounts
}

type Overview struct {
	Enrollment
	Subjects []SubjectOverview
}

// StudentRow is one line of the tutor's student list.
type StudentRow = Enrollment
