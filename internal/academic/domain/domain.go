// Package domain holds the academic entities and their rules. It knows nothing
// about HTTP, SQL or tokens.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

// Role codes as stored by the identity module; academic only compares them.
const (
	RoleDirector = "DIRECTOR"
	RoleTeacher  = "TEACHER"
	RoleTutor    = "TUTOR"
	RoleStudent  = "STUDENT"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }

// Errors returned by the academic module.
var (
	ErrForbidden = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")

	ErrCareerNotFound     = notFound("career_not_found", "career not found")
	ErrSubjectNotFound    = notFound("subject_not_found", "subject not found")
	ErrTermNotFound       = notFound("term_not_found", "academic term not found")
	ErrGroupNotFound      = notFound("group_not_found", "group not found")
	ErrAssignmentNotFound = notFound("teaching_assignment_not_found", "teaching assignment not found")

	ErrUserNotFound = validation("user_not_found", "referenced user does not exist")

	ErrCareerCodeTaken       = conflict("career_code_taken", "a career with that code already exists")
	ErrSubjectCodeTaken      = conflict("subject_code_taken", "a subject with that code already exists")
	ErrTermNameTaken         = conflict("term_name_taken", "an academic term with that name already exists")
	ErrUnitNumberTaken       = conflict("unit_number_taken", "the subject already has a unit with that number")
	ErrCurriculumExists      = conflict("curriculum_entry_exists", "the subject is already in that career at that level")
	ErrGroupCodeTaken        = conflict("group_code_taken", "the term already has a group with that code")
	ErrAlreadyEnrolled       = conflict("already_enrolled", "the student is already enrolled in this group")
	ErrAssignmentExists      = conflict("teaching_assignment_exists", "the subject is already assigned to this group")
	ErrScheduleOverlap       = conflict("schedule_overlap", "the schedule overlaps another one of the same teaching assignment")
	ErrSubjectEnrollExists   = conflict("subject_enrollment_exists", "the student is already enrolled in this teaching assignment")
	ErrTermClosed            = conflict("term_closed", "the academic term is closed")
	ErrInvalidTermTransition = conflict("invalid_term_transition", "the term cannot move to that status from its current one")
	ErrCareerInactive        = conflict("career_inactive", "the career is not active")
	ErrSubjectInactive       = conflict("subject_inactive", "the subject is not active")

	ErrNotInCurriculum  = validation("subject_not_in_curriculum", "the subject is not in the group's career curriculum at the group's level")
	ErrStudentNotInGrp  = validation("student_not_in_group", "the student has no active enrollment in the group of this teaching assignment")
	ErrInvalidAttempt   = validation("invalid_attempt_number", "attempt_number must be the next attempt for this student and subject")
	ErrInvalidDates     = validation("invalid_dates", "start_date must be before end_date")
	ErrInvalidLevel     = validation("invalid_level", "level must be a positive integer")
	ErrInvalidDay       = validation("invalid_day_of_week", "day_of_week must be between 1 (Monday) and 7 (Sunday)")
	ErrInvalidTime      = validation("invalid_time", "time must use the HH:MM format")
	ErrInvalidTimeRange = validation("invalid_time_range", "start_time must be before end_time")
	ErrInvalidTutor     = validation("invalid_tutor", "the user must be an active TUTOR")
	ErrInvalidTeacher   = validation("invalid_teacher", "the user must be an active TEACHER")
	ErrInvalidStudent   = validation("invalid_student", "the user must be an active STUDENT")
)

// --- Catalogs ---------------------------------------------------------------

type Career struct {
	ID       uuid.UUID
	Code     string
	Name     string
	IsActive bool
}

type Subject struct {
	ID       uuid.UUID
	Code     string
	Name     string
	IsActive bool
}

type SubjectUnit struct {
	ID        uuid.UUID
	SubjectID uuid.UUID
	Number    int16
	Name      string
}

// CurriculumEntry places a subject at a level of a career's plan.
type CurriculumEntry struct {
	CareerID    uuid.UUID
	SubjectID   uuid.UUID
	Level       int16
	SubjectCode string
	SubjectName string
}

const (
	maxCodeLen      = 30
	maxCareerName   = 180
	maxSubjectName  = 180
	maxTermName     = 80
	maxClassroomLen = 50
	maxLevel        = 32767 // SMALLINT
)

func checkText(value string, max int, code, label string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", validation(code+"_required", label+" is required")
	}
	if utf8.RuneCountInString(v) > max {
		return "", validation(code+"_too_long", label+" is too long")
	}
	return v, nil
}

// NewCode normalizes and validates a catalog code (careers, subjects, groups).
func NewCode(value string) (string, error) {
	v, err := checkText(value, maxCodeLen, "code", "code")
	return strings.ToUpper(v), err
}

func NewCareerName(value string) (string, error) {
	return checkText(value, maxCareerName, "name", "name")
}

func NewSubjectName(value string) (string, error) {
	return checkText(value, maxSubjectName, "name", "name")
}

func ValidateLevel(level int) error {
	if level < 1 || level > maxLevel {
		return ErrInvalidLevel
	}
	return nil
}

func ValidateUnitNumber(n int) error {
	if n < 1 || n > maxLevel {
		return validation("invalid_unit_number", "unit_number must be a positive integer")
	}
	return nil
}

// --- Academic terms -----------------------------------------------------------

type TermStatus string

const (
	TermDraft  TermStatus = "draft"
	TermActive TermStatus = "active"
	TermClosed TermStatus = "closed"
)

// Term is an academic period. Status is its single source of state. The model
// does not restrict how many terms may be active at once.
type Term struct {
	ID        uuid.UUID
	Name      string
	StartDate time.Time // date only (UTC midnight)
	EndDate   time.Time
	Status    TermStatus
}

// NewTerm validates and builds a draft term.
func NewTerm(id uuid.UUID, name string, start, end time.Time) (*Term, error) {
	n, err := checkText(name, maxTermName, "name", "name")
	if err != nil {
		return nil, err
	}
	if !start.Before(end) {
		return nil, ErrInvalidDates
	}
	return &Term{ID: id, Name: n, StartDate: start, EndDate: end, Status: TermDraft}, nil
}

// Transition returns the status reached by moving to target, or
// ErrInvalidTermTransition. Allowed: draft -> active -> closed. Closed is final.
func (t *Term) Transition(target TermStatus) (TermStatus, error) {
	switch {
	case t.Status == TermDraft && target == TermActive:
		return TermActive, nil
	case t.Status == TermActive && target == TermClosed:
		return TermClosed, nil
	}
	return "", ErrInvalidTermTransition
}

// RequireOpen fails with ErrTermClosed once the term has been closed.
func (t *Term) RequireOpen() error {
	if t.Status == TermClosed {
		return ErrTermClosed
	}
	return nil
}

// --- People -------------------------------------------------------------------

// UserRef is what academic needs to know about a user to validate relations.
type UserRef struct {
	ID       uuid.UUID
	IsActive bool
	Roles    []string
}

func (u UserRef) HasRole(role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Person is the public identification of a student or teacher inside academic
// listings. It never carries contact data, CURP or address.
type Person struct {
	ID                  uuid.UUID
	InstitutionalNumber string
	FirstName           string
	LastName            string
}

// --- Groups and enrollments ---------------------------------------------------

type Group struct {
	ID       uuid.UUID
	TermID   uuid.UUID
	CareerID uuid.UUID
	TutorID  *uuid.UUID
	Code     string
	Level    int16

	// Descriptive fields filled when reading.
	TermName   string
	CareerCode string
	CareerName string
}

type EnrollmentStatus string

const (
	EnrollmentActive    EnrollmentStatus = "ACTIVE"
	EnrollmentWithdrawn EnrollmentStatus = "WITHDRAWN"
)

type Enrollment struct {
	ID         uuid.UUID
	GroupID    uuid.UUID
	StudentID  uuid.UUID
	Status     EnrollmentStatus
	EnrolledAt time.Time
	Student    Person
}

// --- Teaching assignments and schedules -----------------------------------------

type TeachingAssignment struct {
	ID        uuid.UUID
	GroupID   uuid.UUID
	SubjectID uuid.UUID
	TeacherID uuid.UUID

	// Context used for authorization and display.
	TermID       uuid.UUID
	GroupTutorID *uuid.UUID
	GroupCode    string
	SubjectCode  string
	SubjectName  string
	TeacherName  string
}

// Schedule is one weekly class slot. Times are "HH:MM" (24 h).
type Schedule struct {
	ID                   uuid.UUID
	TeachingAssignmentID uuid.UUID
	DayOfWeek            int16 // 1 = Monday ... 7 = Sunday
	StartTime            string
	EndTime              string
	Classroom            string // empty when not set
}

// ScheduleEntry is a schedule together with the assignment it belongs to.
type ScheduleEntry struct {
	Schedule
	Assignment TeachingAssignment
}

// ParseClock converts "HH:MM" to minutes after midnight.
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, ErrInvalidTime
	}
	return t.Hour()*60 + t.Minute(), nil
}

// NewSchedule validates a weekly slot and normalizes the classroom.
func NewSchedule(id, assignmentID uuid.UUID, day int, start, end, classroom string) (*Schedule, error) {
	if day < 1 || day > 7 {
		return nil, ErrInvalidDay
	}
	s, err := ParseClock(start)
	if err != nil {
		return nil, err
	}
	e, err := ParseClock(end)
	if err != nil {
		return nil, err
	}
	if s >= e {
		return nil, ErrInvalidTimeRange
	}
	room := strings.TrimSpace(classroom)
	if utf8.RuneCountInString(room) > maxClassroomLen {
		return nil, validation("classroom_too_long", "classroom is too long")
	}
	return &Schedule{ID: id, TeachingAssignmentID: assignmentID, DayOfWeek: int16(day), StartTime: start, EndTime: end, Classroom: room}, nil
}

// Overlaps reports whether two slots of the same day share any time.
func (s Schedule) Overlaps(o Schedule) bool {
	if s.DayOfWeek != o.DayOfWeek {
		return false
	}
	s1, _ := ParseClock(s.StartTime)
	e1, _ := ParseClock(s.EndTime)
	s2, _ := ParseClock(o.StartTime)
	e2, _ := ParseClock(o.EndTime)
	return s1 < e2 && s2 < e1
}

// --- Subject enrollments --------------------------------------------------------

type SubjectEnrollmentStatus string

const (
	SubjectEnrollmentActive    SubjectEnrollmentStatus = "ACTIVE"
	SubjectEnrollmentWithdrawn SubjectEnrollmentStatus = "WITHDRAWN"
)

// SubjectEnrollment links a student with a teaching assignment. It is the
// record Grades and Attendance hang from.
type SubjectEnrollment struct {
	ID                   uuid.UUID
	StudentID            uuid.UUID
	TeachingAssignmentID uuid.UUID
	AttemptNumber        int16
	Status               SubjectEnrollmentStatus
	Student              Person
}
