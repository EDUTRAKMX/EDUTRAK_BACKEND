// Package domain holds the grading entities and rules. It knows nothing about
// HTTP, SQL or tokens.
package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }

// Errors returned by the grades module.
var (
	ErrForbidden = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")

	ErrGradeNotFound      = notFound("grade_not_found", "grade not found")
	ErrStudentNotFound    = notFound("student_not_found", "student not found")
	ErrAssignmentNotFound = notFound("teaching_assignment_not_found", "teaching assignment not found")

	// Referenced from a request body: reported as 422, like in the academic module.
	ErrEnrollmentNotFound = validation("subject_enrollment_not_found", "subject enrollment not found")
	ErrUnitNotFound       = validation("subject_unit_not_found", "subject unit not found")
	ErrUnitNotInSubject   = validation("unit_not_in_subject", "the unit does not belong to the subject of this teaching assignment")

	ErrInvalidScore   = validation("invalid_score", "score must be a number between 0 and 100 with at most two decimals")
	ErrScorePrecision = validation("invalid_score_precision", "score cannot have more than two decimals")
	ErrScoreRange     = validation("score_out_of_range", "score must be between 0.00 and 100.00")

	ErrGradeExists         = conflict("grade_exists", "a grade already exists for this student attempt and unit; use PATCH to correct it")
	ErrTermClosed          = conflict("term_closed", "the academic term is closed")
	ErrEnrollmentNotActive = conflict("subject_enrollment_inactive", "the subject enrollment is not active")
)

// --- Score ---------------------------------------------------------------------------

// Score is a grade on the 0..100 scale stored as an exact number of hundredths
// (89.99 -> 8999). No floating point is involved at any point, so classification
// and comparisons never suffer rounding errors. The zero value is a real 0.00;
// "not captured yet" is represented by a nil *Score, never by zero.
type Score struct{ hundredths int32 }

const maxHundredths = 10000

// NewScoreFromHundredths builds a score from hundredths (0..10000).
func NewScoreFromHundredths(h int64) (Score, error) {
	if h < 0 || h > maxHundredths {
		return Score{}, ErrScoreRange
	}
	return Score{int32(h)}, nil
}

// ParseScore reads a decimal such as "89.99", "90" or "100.00". It rejects
// signs, exponents, thousands separators and anything with more than two
// significant decimals (extra trailing zeros are accepted: "90.000" is 90.00).
// Values outside 0..100 fail with ErrScoreRange; nothing is ever rounded.
func ParseScore(s string) (Score, error) {
	if s == "" {
		return Score{}, ErrInvalidScore
	}
	negative := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	intPart, frac, hasDot := strings.Cut(body, ".")
	if !allDigits(intPart) || (hasDot && !allDigits(frac)) || len(intPart) > 6 {
		return Score{}, ErrInvalidScore
	}
	if len(frac) > 2 {
		if strings.Trim(frac[2:], "0") != "" {
			return Score{}, ErrScorePrecision
		}
		frac = frac[:2]
	}
	for len(frac) < 2 {
		frac += "0"
	}
	var whole int64
	for _, c := range intPart {
		whole = whole*10 + int64(c-'0')
	}
	h := whole*100 + int64(frac[0]-'0')*10 + int64(frac[1]-'0')
	if negative {
		return Score{}, ErrScoreRange // "-0.00" included: a sign is never valid input
	}
	return NewScoreFromHundredths(h)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Hundredths returns the exact value in hundredths.
func (s Score) Hundredths() int32 { return s.hundredths }

// String formats the score with exactly two decimals ("90.00"), matching NUMERIC(5,2).
func (s Score) String() string {
	return fmt.Sprintf("%d.%02d", s.hundredths/100, s.hundredths%100)
}

// Category is the competency level derived from a score.
type Category string

const (
	CategoryNA Category = "NA" // < 80.00
	CategorySA Category = "SA" // 80.00 <= score < 90.00
	CategoryDE Category = "DE" // 90.00 <= score < 100.00
	CategoryAU Category = "AU" // exactly 100.00
)

// Category classifies the exact score; it never rounds first.
func (s Score) Category() Category {
	switch {
	case s.hundredths < 8000:
		return CategoryNA
	case s.hundredths < 9000:
		return CategorySA
	case s.hundredths < 10000:
		return CategoryDE
	default:
		return CategoryAU
	}
}

// --- Entities ------------------------------------------------------------------------

// Grade is the score of one subject enrollment (one student attempt) in one unit.
type Grade struct {
	ID                  uuid.UUID
	SubjectEnrollmentID uuid.UUID
	SubjectUnitID       uuid.UUID
	Score               Score
	RecordedBy          uuid.UUID
	RecordedAt          time.Time
	UpdatedAt           time.Time
}

// TermClosedStatus and the enrollment statuses mirror the values academic stores.
const (
	TermStatusClosed       = "closed"
	EnrollmentStatusActive = "ACTIVE"
)

// SubjectEnrollmentRef is what grading needs to know about an enrollment and its
// teaching assignment (read from academic data).
type SubjectEnrollmentRef struct {
	ID            uuid.UUID
	StudentID     uuid.UUID
	AssignmentID  uuid.UUID
	TeacherID     uuid.UUID
	GroupID       uuid.UUID
	GroupTutorID  *uuid.UUID
	SubjectID     uuid.UUID
	TermID        uuid.UUID
	TermStatus    string
	Status        string
	AttemptNumber int16
}

// RequireWritable fails if grades of this enrollment can no longer be changed.
func (r SubjectEnrollmentRef) RequireWritable() error {
	if r.TermStatus == TermStatusClosed {
		return ErrTermClosed
	}
	if r.Status != EnrollmentStatusActive {
		return ErrEnrollmentNotActive
	}
	return nil
}

type Unit struct {
	ID        uuid.UUID
	SubjectID uuid.UUID
	Number    int16
	Name      string
}

// Person identifies a student; it never carries contact data.
type Person struct {
	ID                  uuid.UUID
	InstitutionalNumber string
	FirstName           string
	LastName            string
}

// UnitGrade is a unit with its grade, or Grade == nil when it is still pending.
type UnitGrade struct {
	Unit      Unit
	GradeID   *uuid.UUID
	Score     *Score
	UpdatedAt *time.Time
}

// StudentSheet is one attempt of a student (a subject enrollment) with every
// unit of the subject, captured or pending.
type StudentSheet struct {
	SubjectEnrollmentID uuid.UUID
	AssignmentID        uuid.UUID
	AttemptNumber       int16
	Status              string
	TermID              uuid.UUID
	TermName            string
	GroupCode           string
	SubjectID           uuid.UUID
	SubjectCode         string
	SubjectName         string
	Units               []UnitGrade
}

// AssignmentRow is one student attempt of a teaching assignment; Units aligns
// with AssignmentSheet.Units.
type AssignmentRow struct {
	SubjectEnrollmentID uuid.UUID
	Student             Person
	AttemptNumber       int16
	Status              string
	Units               []UnitGrade
}

// AssignmentSheet is the teacher's grade table: students x units.
type AssignmentSheet struct {
	AssignmentID uuid.UUID
	GroupID      uuid.UUID
	GroupCode    string
	SubjectID    uuid.UUID
	SubjectCode  string
	SubjectName  string
	TermID       uuid.UUID
	TermStatus   string
	Units        []Unit
	Rows         []AssignmentRow
}
