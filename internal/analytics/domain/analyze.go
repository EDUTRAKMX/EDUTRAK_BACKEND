package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// AlertStatus mirrors alerts.status.
type AlertStatus string

const (
	AlertOpen         AlertStatus = "OPEN"
	AlertAcknowledged AlertStatus = "ACKNOWLEDGED"
	AlertResolved     AlertStatus = "RESOLVED"
)

// StoredAlert is a persisted alert. Evidence is the JSON document stored with it.
type StoredAlert struct {
	ID                  uuid.UUID
	StudentID           uuid.UUID
	GroupID             uuid.UUID
	SubjectEnrollmentID *uuid.UUID
	Rule                Rule
	Key                 string
	Message             string
	Evidence            []byte
	Status              AlertStatus
	CreatedAt           time.Time
	UpdatedAt           time.Time
	EvaluatedAt         time.Time
	ResolvedAt          *time.Time
}

// AlertView is a stored alert with the names needed to display it and to authorize it.
type AlertView struct {
	StoredAlert
	StudentNumber string
	StudentFirst  string
	StudentLast   string
	GroupCode     string
	GroupTutorID  *uuid.UUID // CURRENT tutor of the group (authorization)
	TermID        uuid.UUID
	TermName      string
	SubjectCode   string
	SubjectName   string
}

// --- per-student / per-subject analytics ---------------------------------------------------------

type SubjectAnalytics struct {
	Facts      SubjectFacts
	Grades     GradeStats
	Attendance AttendanceStats
	Trend      *Trend
	Requests   RequestStats
	NAUnits    []UnitFact
}

// AttendanceTotals sums subjects. LATE equivalences are applied per subject (each with its
// own configured N) and then added, so an absence is never counted twice.
type AttendanceTotals struct {
	AttendanceStats
	AbsencesFromLates int
	EffectiveAbsences int
}

type StudentAnalytics struct {
	Student    StudentFacts
	Subjects   []SubjectAnalytics
	Grades     GradeStats
	Attendance AttendanceTotals
	Requests   RequestStats
	Signals    []Candidate
}

func analyzeSubject(s SubjectFacts, trendWindow int) SubjectAnalytics {
	a := SubjectAnalytics{Facts: s, Grades: s.GradeStats(), Attendance: s.Attendance(), Trend: s.ComputeTrend(trendWindow), Requests: s.RequestStats()}
	for _, u := range s.Units {
		if cat, ok := unitCategory(u); ok && cat == "NA" {
			a.NAUnits = append(a.NAUnits, u)
		}
	}
	return a
}

// Analyze derives every indicator and the live signals of one student. Totals only add the
// ACTIVE subject enrollments; withdrawn ones still appear in Subjects with their own figures.
func Analyze(g GroupInfo, st StudentFacts, rules Rules) StudentAnalytics {
	out := StudentAnalytics{Student: st, Subjects: []SubjectAnalytics{}, Signals: Evaluate(g, st, rules)}
	for _, s := range st.Subjects {
		a := analyzeSubject(s, rules.TrendWindow)
		out.Subjects = append(out.Subjects, a)
		if s.Status != "ACTIVE" {
			continue
		}
		out.Grades.Add(a.Grades)
		out.Attendance.Add(a.Attendance)
		out.Attendance.AbsencesFromLates += a.Attendance.AbsencesFromLates()
		out.Attendance.EffectiveAbsences += a.Attendance.EffectiveAbsences()
		out.Requests.Add(a.Requests)
	}
	return out
}

// --- group analytics -------------------------------------------------------------------------------

// AssignmentAnalytics aggregates one teaching assignment across the students of the group.
// Its denominators are those of its own records; assignments are never merged with each other.
type AssignmentAnalytics struct {
	AssignmentID uuid.UUID
	SubjectCode  string
	SubjectName  string
	Students     int // active subject enrollments
	Grades       GradeStats
	Attendance   AttendanceStats
	Requests     RequestStats
}

type StudentRow struct {
	StudentID                          uuid.UUID
	Number, First, Last                string
	AttendanceRate                     Ratio
	UnjustifiedAbsences, EffectiveAbs  int
	NA, PendingUnits                   int
	PendingRequests, FollowUps, Signal int
}

type GroupAnalytics struct {
	Group                GroupInfo
	Students             int
	Grades               GradeStats
	Attendance           AttendanceTotals
	Requests             RequestStats
	StudentsWithFollowUp int
	FollowUps            int
	ByAssignment         []AssignmentAnalytics
	Rows                 []StudentRow
	SignalsByRule        map[Rule]int
}

// AnalyzeGroup aggregates ACTIVE students. Percentages are computed from summed numerators and
// denominators (never by averaging percentages).
func AnalyzeGroup(g GroupInfo, students []StudentFacts, rules Rules) GroupAnalytics {
	out := GroupAnalytics{Group: g, ByAssignment: []AssignmentAnalytics{}, Rows: []StudentRow{}, SignalsByRule: map[Rule]int{}}
	byAssignment := map[uuid.UUID]*AssignmentAnalytics{}
	for _, st := range students {
		if st.EnrollmentStatus != "ACTIVE" {
			continue
		}
		out.Students++
		sa := Analyze(g, st, rules)
		out.Grades.Add(sa.Grades)
		out.Attendance.Add(sa.Attendance.AttendanceStats)
		out.Attendance.AbsencesFromLates += sa.Attendance.AbsencesFromLates
		out.Attendance.EffectiveAbsences += sa.Attendance.EffectiveAbsences
		out.Requests.Add(sa.Requests)
		if st.FollowUps.Count > 0 {
			out.StudentsWithFollowUp++
		}
		out.FollowUps += st.FollowUps.Count
		for _, c := range sa.Signals {
			out.SignalsByRule[c.Rule]++
		}
		for _, s := range sa.Subjects {
			if s.Facts.Status != "ACTIVE" {
				continue
			}
			a := byAssignment[s.Facts.AssignmentID]
			if a == nil {
				a = &AssignmentAnalytics{AssignmentID: s.Facts.AssignmentID, SubjectCode: s.Facts.SubjectCode, SubjectName: s.Facts.SubjectName}
				byAssignment[a.AssignmentID] = a
			}
			a.Students++
			a.Grades.Add(s.Grades)
			a.Attendance.Add(s.Attendance)
			a.Requests.Add(s.Requests)
		}
		out.Rows = append(out.Rows, StudentRow{
			StudentID: st.StudentID, Number: st.Number, First: st.First, Last: st.Last,
			AttendanceRate: sa.Attendance.Rate(), UnjustifiedAbsences: sa.Attendance.Absent, EffectiveAbs: sa.Attendance.EffectiveAbsences,
			NA: sa.Grades.NA, PendingUnits: sa.Grades.Pending, PendingRequests: sa.Requests.PendingTutor + sa.Requests.PendingTeacher,
			FollowUps: st.FollowUps.Count, Signal: len(sa.Signals),
		})
	}
	for _, a := range byAssignment {
		out.ByAssignment = append(out.ByAssignment, *a)
	}
	sort.Slice(out.ByAssignment, func(i, j int) bool {
		if out.ByAssignment[i].SubjectCode != out.ByAssignment[j].SubjectCode {
			return out.ByAssignment[i].SubjectCode < out.ByAssignment[j].SubjectCode
		}
		return out.ByAssignment[i].AssignmentID.String() < out.ByAssignment[j].AssignmentID.String()
	})
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].Last != out.Rows[j].Last {
			return out.Rows[i].Last < out.Rows[j].Last
		}
		if out.Rows[i].First != out.Rows[j].First {
			return out.Rows[i].First < out.Rows[j].First
		}
		return out.Rows[i].StudentID.String() < out.Rows[j].StudentID.String()
	})
	return out
}

// GroupOverview is one line of the director's overview. It is computed by SQL aggregates and
// uses the same definitions as AnalyzeGroup.
type GroupOverview struct {
	Group                GroupInfo
	ActiveStudents       int
	Grades               GradeStats
	Attendance           AttendanceStats
	Requests             RequestStats
	StudentsWithFollowUp int
	OpenAlerts           int
}
