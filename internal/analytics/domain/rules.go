package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Rule codes stored in alerts.type.
type Rule string

const (
	RuleGradeNA             Rule = "GRADE_NA_UNIT"
	RuleUnjustifiedAbsences Rule = "UNJUSTIFIED_ABSENCES"
	RuleAttendanceDrop      Rule = "ATTENDANCE_DROP"
	RulePendingJustifs      Rule = "PENDING_JUSTIFICATIONS"
)

var ruleDescriptions = map[Rule]string{
	RuleGradeNA:             "A captured unit score is below 80.00 (category NA).",
	RuleUnjustifiedAbsences: "ABSENT records of closed sessions in one subject reach the configured threshold.",
	RuleAttendanceDrop:      "The attendance rate of the last N closed sessions is at least the configured number of points below the N sessions before.",
	RulePendingJustifs:      "The student has justification requests waiting for the Tutor or the Teacher.",
}

// Describe is the human description of a rule (shown next to every alert).
func (r Rule) Describe() string { return ruleDescriptions[r] }

func (r Rule) Valid() bool { _, ok := ruleDescriptions[r]; return ok }

// Rules are the thresholds the alerts use. They are TECHNICAL PROPOSALS for the MVP, not
// institutional policy; they come from configuration and can change without touching any
// other module.
type Rules struct {
	AbsencesThreshold int // UNJUSTIFIED_ABSENCES: ABSENT records needed (>= 1)
	TrendWindow       int // ATTENDANCE_DROP: sessions per window (>= 1)
	TrendDropPoints   int // ATTENDANCE_DROP: percentage points of fall (1..100)
}

func DefaultRules() Rules { return Rules{AbsencesThreshold: 3, TrendWindow: 5, TrendDropPoints: 20} }

func (r Rules) Valid() bool {
	return r.AbsencesThreshold >= 1 && r.TrendWindow >= 1 && r.TrendWindow <= 50 && r.TrendDropPoints >= 1 && r.TrendDropPoints <= 100
}

// Candidate is an alert condition that currently holds, with everything needed to explain it.
// (StudentID, Rule, Key) identify the condition; the same condition always yields the same key,
// which is what makes re-evaluation idempotent.
type Candidate struct {
	Rule                Rule
	Key                 string
	StudentID           uuid.UUID
	SubjectEnrollmentID *uuid.UUID
	Message             string
	Evidence            map[string]any
}

func ptr(id uuid.UUID) *uuid.UUID { return &id }

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

func pct(h int) string { return FormatHundredths(h) + "%" }

// Evaluate returns the alert conditions that hold for one student in one group, in a
// deterministic order. Only ACTIVE enrollments generate alerts, and only facts that are
// final: captured unit scores, records of CLOSED sessions and submitted requests.
func Evaluate(g GroupInfo, st StudentFacts, rules Rules) []Candidate {
	if st.EnrollmentStatus != "ACTIVE" {
		return nil
	}
	var out []Candidate
	var pendingTutor, pendingTeacher int
	var oldest *time.Time
	ctx := func(s SubjectFacts) map[string]any {
		return map[string]any{
			"group_id": g.ID.String(), "group_code": g.Code, "academic_term": g.TermName,
			"subject_enrollment_id": s.SubjectEnrollmentID.String(), "subject_code": s.SubjectCode, "subject": s.SubjectName,
			"attempt_number": s.Attempt,
		}
	}

	subjects := append([]SubjectFacts(nil), st.Subjects...)
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].SubjectCode != subjects[j].SubjectCode {
			return subjects[i].SubjectCode < subjects[j].SubjectCode
		}
		return subjects[i].Attempt < subjects[j].Attempt
	})

	for _, s := range subjects {
		rs := s.RequestStats()
		pendingTutor += rs.PendingTutor
		pendingTeacher += rs.PendingTeacher
		if rs.OldestPending != nil && (oldest == nil || rs.OldestPending.Before(*oldest)) {
			t := *rs.OldestPending
			oldest = &t
		}
		if s.Status != "ACTIVE" {
			continue
		}
		se := s.SubjectEnrollmentID

		// A. NA units
		for _, u := range s.Units {
			cat, ok := unitCategory(u)
			if !ok || cat != "NA" {
				continue
			}
			score, _, _ := UnitScore(u)
			ev := ctx(s)
			ev["subject_unit_id"], ev["unit_number"], ev["unit_name"] = u.UnitID.String(), u.Number, u.Name
			ev["score"], ev["category"] = score, "NA"
			out = append(out, Candidate{
				Rule: RuleGradeNA, Key: fmt.Sprintf("unit:%s:%s", se, u.UnitID), StudentID: st.StudentID, SubjectEnrollmentID: ptr(se),
				Message:  fmt.Sprintf("El alumno tiene una calificación de %s (NA) en la Unidad %d de %s.", score, u.Number, s.SubjectName),
				Evidence: ev,
			})
		}

		att := s.Attendance()
		// B. unjustified absences
		if att.Absent >= rules.AbsencesThreshold {
			pendingReq := map[uuid.UUID]bool{}
			for _, q := range s.Requests {
				if q.Status == ReqPendingTutor || q.Status == ReqPendingTeacher {
					pendingReq[q.RecordID] = true
				}
			}
			withPending := 0
			for _, r := range s.Records {
				if r.Status == "ABSENT" && pendingReq[r.RecordID] {
					withPending++
				}
			}
			ev := ctx(s)
			ev["unjustified_absences"], ev["threshold"] = att.Absent, rules.AbsencesThreshold
			ev["closed_session_records"], ev["justified_absences"], ev["lates"] = att.Considered(), att.Justified, att.Late
			ev["absences_with_pending_request"] = withPending
			if att.LatesPerAbsence != nil {
				ev["lates_per_absence"], ev["effective_absences"] = *att.LatesPerAbsence, att.EffectiveAbsences()
			}
			out = append(out, Candidate{
				Rule: RuleUnjustifiedAbsences, Key: "absences:" + se.String(), StudentID: st.StudentID, SubjectEnrollmentID: ptr(se),
				Message: fmt.Sprintf("El alumno acumula %d ausencias sin justificar en %s (umbral de la regla: %d; sesiones cerradas consideradas: %d).",
					att.Absent, s.SubjectName, rules.AbsencesThreshold, att.Considered()),
				Evidence: ev,
			})
		}

		// C. attendance drop
		if tr := s.ComputeTrend(rules.TrendWindow); tr != nil && tr.DropHundredths >= rules.TrendDropPoints*100 {
			ev := ctx(s)
			ev["window_sessions"], ev["threshold_points"] = tr.Window, rules.TrendDropPoints
			ev["recent_pct"], ev["previous_pct"], ev["drop_points"] = FormatHundredths(tr.RecentPct), FormatHundredths(tr.PreviousPct), FormatHundredths(tr.DropHundredths)
			ev["recent"] = map[string]any{"present": tr.Recent.Present, "late": tr.Recent.Late, "absent": tr.Recent.Absent, "justified": tr.Recent.Justified}
			ev["previous"] = map[string]any{"present": tr.Previous.Present, "late": tr.Previous.Late, "absent": tr.Previous.Absent, "justified": tr.Previous.Justified}
			out = append(out, Candidate{
				Rule: RuleAttendanceDrop, Key: "drop:" + se.String(), StudentID: st.StudentID, SubjectEnrollmentID: ptr(se),
				Message: fmt.Sprintf("En %s, la asistencia fue %s en las últimas %d sesiones cerradas y %s en las %d anteriores (descenso de %s puntos).",
					s.SubjectName, pct(tr.RecentPct), tr.Window, pct(tr.PreviousPct), tr.Window, FormatHundredths(tr.DropHundredths)),
				Evidence: ev,
			})
		}
	}

	// D. pending justification requests (one alert per student and group)
	if pendingTutor+pendingTeacher > 0 {
		out = append(out, Candidate{
			Rule: RulePendingJustifs, Key: "pending:" + g.ID.String(), StudentID: st.StudentID,
			Message: fmt.Sprintf("El alumno tiene %d solicitudes de justificación pendientes del Tutor y %d pendientes del Profesor; la más antigua se registró el %s.",
				pendingTutor, pendingTeacher, day(*oldest)),
			Evidence: map[string]any{
				"group_id": g.ID.String(), "group_code": g.Code, "academic_term": g.TermName,
				"pending_tutor": pendingTutor, "pending_teacher": pendingTeacher, "oldest_pending_request_date": day(*oldest),
			},
		})
	}
	return out
}
