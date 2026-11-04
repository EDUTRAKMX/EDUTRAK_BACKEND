// Package domain holds the analytics model: facts read from the other modules, the
// deterministic indicators derived from them and the explicit alert rules. It knows nothing
// about HTTP, SQL or JSON, and it never invents institutional policy: every threshold is an
// explicit, configurable input (see Rules).
package domain

import (
	"strconv"
	"time"

	attendancedomain "github.com/edutrak/backend/internal/attendance/domain"
	gradesdomain "github.com/edutrak/backend/internal/grades/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }

var (
	ErrForbidden = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")

	ErrStudentNotFound = notFound("student_not_found", "student not found")
	ErrGroupNotFound   = notFound("group_not_found", "group not found")
	ErrContextNotFound = notFound("enrollment_not_found", "the student has no enrollment in that group")
	ErrAlertNotFound   = notFound("alert_not_found", "alert not found")

	ErrInvalidFilter  = validation("invalid_filter", "invalid filter value")
	ErrInvalidPaging  = validation("invalid_paging", "limit must be 1-200 and offset >= 0")
	ErrGroupRequired  = validation("group_id_required", "group_id is required")
	ErrInvalidStudent = validation("invalid_student_id", "student_id must be a valid UUID")
	ErrInvalidGroup   = validation("invalid_group_id", "group_id must be a valid UUID")

	ErrTermClosed = conflict("term_closed", "the academic term is closed")
)

// --- numbers ------------------------------------------------------------------------------------

// Ratio is Num/Den with exact integers. A ratio with Den == 0 has NO value: it is reported
// as absent (null), never as 0%.
type Ratio struct{ Num, Den int }

// Hundredths is the percentage in hundredths of a point (8333 = 83.33%), rounded half up with
// integer arithmetic. ok is false without a denominator.
func (r Ratio) Hundredths() (h int, ok bool) {
	if r.Den <= 0 {
		return 0, false
	}
	return (r.Num*20000 + r.Den) / (2 * r.Den), true
}

// FormatHundredths renders 8333 as "83.33" (the way Grades renders scores: no floats).
func FormatHundredths(h int) string {
	sign := ""
	if h < 0 {
		sign, h = "-", -h
	}
	frac := strconv.Itoa(h % 100)
	if len(frac) == 1 {
		frac = "0" + frac
	}
	return sign + strconv.Itoa(h/100) + "." + frac
}

// --- facts (read from the other modules; nothing here is stored by Analytics) -------------------

type GroupInfo struct {
	ID         uuid.UUID
	Code       string
	Level      int
	TutorID    *uuid.UUID
	CareerID   uuid.UUID
	CareerCode string
	CareerName string
	TermID     uuid.UUID
	TermName   string
	TermStatus string
	TermStart  time.Time
	TermEnd    time.Time
}

// EnrollmentRef is one enrollment of a student in a group (used to resolve the context).
type EnrollmentRef struct {
	GroupInfo
	StudentID uuid.UUID
	Status    string
}

type UnitFact struct {
	UnitID     uuid.UUID
	Number     int
	Name       string
	Hundredths *int32 // nil = not captured: pending, never 0.00
}

// RecordFact is one attendance record of a CLOSED session (open sessions are excluded: until
// the close, the absence of a record does not mean the student was absent).
type RecordFact struct {
	RecordID uuid.UUID
	Status   attendancedomain.RecordStatus
	Date     time.Time
	OpenedAt time.Time
}

type RequestStatus string

const (
	ReqPendingTutor    RequestStatus = "PENDING_TUTOR"
	ReqPendingTeacher  RequestStatus = "PENDING_TEACHER"
	ReqTutorRejected   RequestStatus = "TUTOR_REJECTED"
	ReqTeacherRejected RequestStatus = "TEACHER_REJECTED"
	ReqJustified       RequestStatus = "JUSTIFIED"
)

type RequestFact struct {
	RequestID uuid.UUID
	RecordID  uuid.UUID
	Status    RequestStatus
	CreatedAt time.Time
}

type SubjectFacts struct {
	SubjectEnrollmentID uuid.UUID
	AssignmentID        uuid.UUID
	SubjectCode         string
	SubjectName         string
	Attempt             int
	Status              string // subject enrollment: ACTIVE | WITHDRAWN
	LatesPerAbsence     *int   // from the most recently opened session; nil without sessions
	Units               []UnitFact
	Records             []RecordFact // chronological
	Requests            []RequestFact
}

type FollowUpFacts struct {
	Count int
	Last  *time.Time
}

type StudentFacts struct {
	StudentID        uuid.UUID
	Number           string
	First, Last      string
	EnrollmentStatus string
	Subjects         []SubjectFacts
	FollowUps        FollowUpFacts
}

// --- indicators -----------------------------------------------------------------------------------

// GradeStats counts the units of one subject. Pending units are NOT counted as NA.
type GradeStats struct {
	Total, Graded, Pending int
	NA, SA, DE, AU         int
}

func (s SubjectFacts) GradeStats() GradeStats {
	g := GradeStats{Total: len(s.Units)}
	for _, u := range s.Units {
		cat, ok := unitCategory(u)
		if !ok {
			g.Pending++
			continue
		}
		g.Graded++
		switch cat {
		case gradesdomain.CategoryNA:
			g.NA++
		case gradesdomain.CategorySA:
			g.SA++
		case gradesdomain.CategoryDE:
			g.DE++
		case gradesdomain.CategoryAU:
			g.AU++
		}
	}
	return g
}

func unitCategory(u UnitFact) (gradesdomain.Category, bool) {
	if u.Hundredths == nil {
		return "", false
	}
	sc, err := gradesdomain.NewScoreFromHundredths(int64(*u.Hundredths))
	if err != nil {
		return "", false
	}
	return sc.Category(), true
}

// UnitScore renders the captured score ("76.00") and its category.
func UnitScore(u UnitFact) (score string, category string, ok bool) {
	if u.Hundredths == nil {
		return "", "", false
	}
	sc, err := gradesdomain.NewScoreFromHundredths(int64(*u.Hundredths))
	if err != nil {
		return "", "", false
	}
	return sc.String(), string(sc.Category()), true
}

// AttendanceStats tallies the records of CLOSED sessions.
//
//	Considered         = Present + Late + Absent + Justified   (records of closed sessions)
//	UnjustifiedAbsent  = Absent  (ABSENT records; JUSTIFIED is a different status)
//	AttendancePct      = (Present + Late) / (Present + Late + Absent)
//
// LATE counts as attended (the student was there). JUSTIFIED is neither attended nor held
// against the student: it leaves the denominator. A subject without closed records has
// AttendancePct == nil (no data), never 0%.
type AttendanceStats struct {
	Present, Late, Absent, Justified int
	LatesPerAbsence                  *int
}

func (a AttendanceStats) Considered() int { return a.Present + a.Late + a.Absent + a.Justified }

func (a AttendanceStats) Rate() Ratio {
	return Ratio{Num: a.Present + a.Late, Den: a.Present + a.Late + a.Absent}
}

func (a AttendanceStats) counts() attendancedomain.Counts {
	return attendancedomain.Counts{Present: a.Present, Late: a.Late, Absent: a.Absent, Justified: a.Justified}
}

// AbsencesFromLates / EffectiveAbsences apply the Attendance rule verbatim (N lates = 1 absence,
// an indicator only; lates are never rewritten as absences and justified records add nothing).
// Without a configured equivalence (no sessions) nothing is derived.
func (a AttendanceStats) AbsencesFromLates() int {
	if a.LatesPerAbsence == nil {
		return 0
	}
	return a.counts().AbsencesFromLates(*a.LatesPerAbsence)
}

func (a AttendanceStats) EffectiveAbsences() int {
	if a.LatesPerAbsence == nil {
		return a.Absent
	}
	return a.counts().EffectiveAbsences(*a.LatesPerAbsence)
}

func tally(records []RecordFact) AttendanceStats {
	var a AttendanceStats
	for _, r := range records {
		switch r.Status {
		case attendancedomain.StatusPresent:
			a.Present++
		case attendancedomain.StatusLate:
			a.Late++
		case attendancedomain.StatusAbsent:
			a.Absent++
		case attendancedomain.StatusJustified:
			a.Justified++
		}
	}
	return a
}

func (s SubjectFacts) Attendance() AttendanceStats {
	a := tally(s.Records)
	a.LatesPerAbsence = s.LatesPerAbsence
	return a
}

// Trend compares the attendance rate of the last `window` closed sessions with the `window`
// sessions before them. It exists only with at least 2*window records, and both windows need a
// denominator (a window made only of justified absences cannot be compared).
type Trend struct {
	Window         int
	Recent         AttendanceStats
	Previous       AttendanceStats
	RecentPct      int // hundredths
	PreviousPct    int // hundredths
	DropHundredths int // Previous - Recent (positive = attendance fell)
}

// ComputeTrend returns nil when there is not enough data.
func (s SubjectFacts) ComputeTrend(window int) *Trend {
	n := len(s.Records)
	if window < 1 || n < 2*window {
		return nil
	}
	recent, previous := tally(s.Records[n-window:]), tally(s.Records[n-2*window:n-window])
	rp, ok1 := recent.Rate().Hundredths()
	pp, ok2 := previous.Rate().Hundredths()
	if !ok1 || !ok2 {
		return nil
	}
	return &Trend{Window: window, Recent: recent, Previous: previous, RecentPct: rp, PreviousPct: pp, DropHundredths: pp - rp}
}

// RequestStats counts REQUESTS and ABSENCES separately: several rejected requests for one
// absence are several requests but a single absence.
type RequestStats struct {
	Total, PendingTutor, PendingTeacher, TutorRejected, TeacherRejected, Approved int
	AbsencesWithRequest                                                           int // distinct attendance records
	OldestPending                                                                 *time.Time
}

func (s SubjectFacts) RequestStats() RequestStats {
	var r RequestStats
	seen := map[uuid.UUID]bool{}
	for _, q := range s.Requests {
		r.Total++
		seen[q.RecordID] = true
		switch q.Status {
		case ReqPendingTutor, ReqPendingTeacher:
			if q.Status == ReqPendingTutor {
				r.PendingTutor++
			} else {
				r.PendingTeacher++
			}
			if r.OldestPending == nil || q.CreatedAt.Before(*r.OldestPending) {
				t := q.CreatedAt
				r.OldestPending = &t
			}
		case ReqTutorRejected:
			r.TutorRejected++
		case ReqTeacherRejected:
			r.TeacherRejected++
		case ReqJustified:
			r.Approved++
		}
	}
	r.AbsencesWithRequest = len(seen)
	return r
}

// Add accumulates another subject's request counters (the oldest pending date is the earliest).
func (r *RequestStats) Add(o RequestStats) {
	r.Total += o.Total
	r.PendingTutor += o.PendingTutor
	r.PendingTeacher += o.PendingTeacher
	r.TutorRejected += o.TutorRejected
	r.TeacherRejected += o.TeacherRejected
	r.Approved += o.Approved
	r.AbsencesWithRequest += o.AbsencesWithRequest
	if o.OldestPending != nil && (r.OldestPending == nil || o.OldestPending.Before(*r.OldestPending)) {
		t := *o.OldestPending
		r.OldestPending = &t
	}
}

func (g *GradeStats) Add(o GradeStats) {
	g.Total += o.Total
	g.Graded += o.Graded
	g.Pending += o.Pending
	g.NA += o.NA
	g.SA += o.SA
	g.DE += o.DE
	g.AU += o.AU
}

func (a *AttendanceStats) Add(o AttendanceStats) {
	a.Present += o.Present
	a.Late += o.Late
	a.Absent += o.Absent
	a.Justified += o.Justified
}
