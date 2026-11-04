package domain

import (
	"strings"
	"testing"
	"time"

	attendancedomain "github.com/edutrak/backend/internal/attendance/domain"
	"github.com/google/uuid"
)

func TestRatioHasNoValueWithoutDenominator(t *testing.T) {
	if _, ok := (Ratio{Num: 0, Den: 0}).Hundredths(); ok {
		t.Fatal("0/0 must be absent, not 0%")
	}
	cases := []struct {
		r    Ratio
		want int
	}{{Ratio{1, 3}, 3333}, {Ratio{2, 3}, 6667}, {Ratio{0, 4}, 0}, {Ratio{4, 4}, 10000}, {Ratio{1, 8}, 1250}, {Ratio{1, 200}, 50}}
	for _, c := range cases {
		got, ok := c.r.Hundredths()
		if !ok || got != c.want {
			t.Errorf("%v -> %d %v, want %d", c.r, got, ok, c.want)
		}
	}
	for h, want := range map[int]string{0: "0.00", 5: "0.05", 8333: "83.33", 10000: "100.00", -2000: "-20.00"} {
		if got := FormatHundredths(h); got != want {
			t.Errorf("FormatHundredths(%d) = %s, want %s", h, got, want)
		}
	}
}

func h32(v int32) *int32 { return &v }

func TestGradeStatsPendingIsNotNAAndBoundariesAreExact(t *testing.T) {
	s := SubjectFacts{Units: []UnitFact{
		{Number: 1, Hundredths: h32(7999)}, {Number: 2, Hundredths: h32(8000)}, {Number: 3, Hundredths: h32(8999)},
		{Number: 4, Hundredths: h32(9000)}, {Number: 5, Hundredths: h32(9999)}, {Number: 6, Hundredths: h32(10000)},
		{Number: 7},                     // not captured
		{Number: 8, Hundredths: h32(0)}, // a real zero is a grade
	}}
	g := s.GradeStats()
	want := GradeStats{Total: 8, Graded: 7, Pending: 1, NA: 2, SA: 2, DE: 2, AU: 1}
	if g != want {
		t.Fatalf("got %+v, want %+v", g, want)
	}
	// 7 units with only 2 captured: 5 pending, never NA.
	two := SubjectFacts{Units: make([]UnitFact, 7)}
	two.Units[0].Hundredths, two.Units[1].Hundredths = h32(9500), h32(8000)
	if g := two.GradeStats(); g.Pending != 5 || g.NA != 0 || g.Graded != 2 {
		t.Fatalf("pending units: %+v", g)
	}
}

var base = time.Date(2026, 2, 2, 8, 0, 0, 0, time.UTC)

func recs(statuses ...string) []RecordFact {
	out := make([]RecordFact, len(statuses))
	for i, s := range statuses {
		out[i] = RecordFact{RecordID: uuid.New(), Status: attendancedomain.RecordStatus(s), Date: base.AddDate(0, 0, i), OpenedAt: base.AddDate(0, 0, i)}
	}
	return out
}

func three() *int { n := 3; return &n }

func TestAttendanceDefinitions(t *testing.T) {
	s := SubjectFacts{LatesPerAbsence: three(), Records: recs("PRESENT", "PRESENT", "LATE", "LATE", "LATE", "LATE", "ABSENT", "JUSTIFIED")}
	a := s.Attendance()
	if a.Considered() != 8 || a.Present != 2 || a.Late != 4 || a.Absent != 1 || a.Justified != 1 {
		t.Fatalf("tally: %+v", a)
	}
	// (present+late) / (present+late+absent): the justified record leaves the denominator.
	if r := a.Rate(); r.Num != 6 || r.Den != 7 {
		t.Fatalf("rate = %+v", r)
	}
	// 4 lates / 3 = 1 absence from lates; effective = 1 absent + 1; justified adds nothing.
	if a.AbsencesFromLates() != 1 || a.EffectiveAbsences() != 2 {
		t.Fatalf("lates %d effective %d", a.AbsencesFromLates(), a.EffectiveAbsences())
	}
	// Without a configured equivalence nothing is derived from lates.
	a.LatesPerAbsence = nil
	if a.AbsencesFromLates() != 0 || a.EffectiveAbsences() != 1 {
		t.Fatalf("without equivalence: %d %d", a.AbsencesFromLates(), a.EffectiveAbsences())
	}
	// No records at all: no rate (null), not 0%.
	if _, ok := (SubjectFacts{}).Attendance().Rate().Hundredths(); ok {
		t.Fatal("a subject without closed records must have no attendance percentage")
	}
	// Only justified absences: still no rate.
	if _, ok := (SubjectFacts{Records: recs("JUSTIFIED", "JUSTIFIED")}).Attendance().Rate().Hundredths(); ok {
		t.Fatal("only justified records leave no denominator")
	}
}

func TestTrendNeedsTwoFullComparableWindows(t *testing.T) {
	good := SubjectFacts{Records: recs("PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "ABSENT", "PRESENT", "ABSENT", "ABSENT")}
	tr := good.ComputeTrend(5)
	if tr == nil || tr.PreviousPct != 10000 || tr.RecentPct != 4000 || tr.DropHundredths != 6000 {
		t.Fatalf("trend: %+v", tr)
	}
	if (&SubjectFacts{Records: recs("PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT")}).ComputeTrend(5) != nil {
		t.Fatal("9 records cannot fill two windows of 5")
	}
	allJustified := SubjectFacts{Records: recs("PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "JUSTIFIED", "JUSTIFIED", "JUSTIFIED", "JUSTIFIED", "JUSTIFIED")}
	if allJustified.ComputeTrend(5) != nil {
		t.Fatal("a window made only of justified absences has no rate to compare")
	}
	if good.ComputeTrend(0) != nil {
		t.Fatal("window 0 is meaningless")
	}
}

func TestRequestStatsCountRequestsAndAbsencesSeparately(t *testing.T) {
	rec1, rec2 := uuid.New(), uuid.New()
	t0 := base
	s := SubjectFacts{Requests: []RequestFact{
		{RequestID: uuid.New(), RecordID: rec1, Status: ReqTutorRejected, CreatedAt: t0},
		{RequestID: uuid.New(), RecordID: rec1, Status: ReqTeacherRejected, CreatedAt: t0.Add(time.Hour)},
		{RequestID: uuid.New(), RecordID: rec1, Status: ReqPendingTeacher, CreatedAt: t0.Add(2 * time.Hour)},
		{RequestID: uuid.New(), RecordID: rec2, Status: ReqPendingTutor, CreatedAt: t0.Add(-time.Hour)},
		{RequestID: uuid.New(), RecordID: rec2, Status: ReqJustified, CreatedAt: t0},
	}}
	r := s.RequestStats()
	if r.Total != 5 || r.AbsencesWithRequest != 2 || r.PendingTutor != 1 || r.PendingTeacher != 1 || r.TutorRejected != 1 || r.TeacherRejected != 1 || r.Approved != 1 {
		t.Fatalf("stats: %+v", r)
	}
	if r.OldestPending == nil || !r.OldestPending.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("oldest pending: %v", r.OldestPending)
	}
}

func group() GroupInfo {
	return GroupInfo{ID: uuid.New(), Code: "2A", TermName: "2026-1", TermStatus: "active"}
}

func student(subjects ...SubjectFacts) StudentFacts {
	return StudentFacts{StudentID: uuid.New(), EnrollmentStatus: "ACTIVE", Subjects: subjects}
}

func mat() SubjectFacts {
	return SubjectFacts{SubjectEnrollmentID: uuid.New(), AssignmentID: uuid.New(), SubjectCode: "MAT1", SubjectName: "Matematicas I", Attempt: 1, Status: "ACTIVE",
		Units: []UnitFact{{UnitID: uuid.New(), Number: 1, Name: "Algebra"}, {UnitID: uuid.New(), Number: 2, Name: "Calculo"}}}
}

func rulesOf(c []Candidate) map[Rule]int {
	m := map[Rule]int{}
	for _, x := range c {
		m[x.Rule]++
	}
	return m
}

func TestEvaluateNAUnit(t *testing.T) {
	g, s := group(), mat()
	s.Units[1].Hundredths = h32(7600)
	s.Units[0].Hundredths = h32(8000) // exactly 80.00 is SA: no alert
	got := Evaluate(g, student(s), DefaultRules())
	if len(got) != 1 || got[0].Rule != RuleGradeNA {
		t.Fatalf("candidates: %+v", got)
	}
	c := got[0]
	if c.Message != "El alumno tiene una calificación de 76.00 (NA) en la Unidad 2 de Matematicas I." {
		t.Fatalf("message: %s", c.Message)
	}
	if c.Evidence["score"] != "76.00" || c.Evidence["category"] != "NA" || c.Evidence["unit_number"] != 2 || c.Evidence["group_code"] != "2A" || c.Evidence["academic_term"] != "2026-1" {
		t.Fatalf("evidence: %v", c.Evidence)
	}
	if !strings.HasPrefix(c.Key, "unit:") || *c.SubjectEnrollmentID != s.SubjectEnrollmentID {
		t.Fatalf("key/se: %s", c.Key)
	}
	// Pending units never raise it; a corrected score removes the condition.
	s.Units[1].Hundredths = nil
	if len(Evaluate(g, student(s), DefaultRules())) != 0 {
		t.Fatal("a pending unit must not raise NA")
	}
	s.Units[1].Hundredths = h32(8100)
	if len(Evaluate(g, student(s), DefaultRules())) != 0 {
		t.Fatal("a corrected score must clear the signal")
	}
}

func TestEvaluateIsDeterministicAndKeysAreStable(t *testing.T) {
	g, s := group(), mat()
	s.Units[0].Hundredths, s.Units[1].Hundredths = h32(1000), h32(2000)
	st := student(s)
	a, b := Evaluate(g, st, DefaultRules()), Evaluate(g, st, DefaultRules())
	if len(a) != 2 || len(a) != len(b) {
		t.Fatalf("len %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Message != b[i].Message {
			t.Fatalf("evaluation is not deterministic at %d", i)
		}
	}
}

func TestEvaluateUnjustifiedAbsencesThreshold(t *testing.T) {
	g, s := group(), mat()
	s.LatesPerAbsence = three()
	s.Records = recs("ABSENT", "ABSENT", "JUSTIFIED", "PRESENT")
	if rulesOf(Evaluate(g, student(s), DefaultRules()))[RuleUnjustifiedAbsences] != 0 {
		t.Fatal("2 unjustified absences are below the threshold of 3 (a justified one does not count)")
	}
	s.Records = recs("ABSENT", "ABSENT", "ABSENT", "JUSTIFIED", "PRESENT")
	got := Evaluate(g, student(s), DefaultRules())
	if rulesOf(got)[RuleUnjustifiedAbsences] != 1 {
		t.Fatalf("candidates: %+v", got)
	}
	ev := got[0].Evidence
	if ev["unjustified_absences"] != 3 || ev["threshold"] != 3 || ev["justified_absences"] != 1 || ev["closed_session_records"] != 5 {
		t.Fatalf("evidence: %v", ev)
	}
	// The threshold is configuration, not a constant.
	r := DefaultRules()
	r.AbsencesThreshold = 4
	if rulesOf(Evaluate(g, student(s), r))[RuleUnjustifiedAbsences] != 0 {
		t.Fatal("a higher configured threshold must silence it")
	}
	// A pending request does not make the absence justified.
	s.Requests = []RequestFact{{RequestID: uuid.New(), RecordID: s.Records[0].RecordID, Status: ReqPendingTeacher, CreatedAt: base}}
	if got := Evaluate(g, student(s), DefaultRules()); rulesOf(got)[RuleUnjustifiedAbsences] != 1 {
		t.Fatal("a pending request must not clear the absence alert")
	}
}

func TestEvaluateAttendanceDrop(t *testing.T) {
	g, s := group(), mat()
	s.Records = recs("PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "ABSENT")
	got := Evaluate(g, student(s), DefaultRules())
	if rulesOf(got)[RuleAttendanceDrop] != 1 {
		t.Fatalf("100%% -> 80%% (20 points) must fire at the default threshold: %+v", got)
	}
	want := "En Matematicas I, la asistencia fue 80.00% en las últimas 5 sesiones cerradas y 100.00% en las 5 anteriores (descenso de 20.00 puntos)."
	if got[0].Message != want {
		t.Fatalf("message: %s", got[0].Message)
	}
	// 100% -> 80% with a configured threshold of 25 points: no alert.
	strict := DefaultRules()
	strict.TrendDropPoints = 25
	if rulesOf(Evaluate(g, student(s), strict))[RuleAttendanceDrop] != 0 {
		t.Fatal("a 20-point drop must not fire a 25-point threshold")
	}
	// Not enough data: no trend alert.
	s.Records = recs("PRESENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT")
	if rulesOf(Evaluate(g, student(s), DefaultRules()))[RuleAttendanceDrop] != 0 {
		t.Fatal("9 records cannot be compared in two windows of 5")
	}
	// Improving attendance never alerts.
	s.Records = recs("ABSENT", "ABSENT", "ABSENT", "ABSENT", "ABSENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT")
	if rulesOf(Evaluate(g, student(s), DefaultRules()))[RuleAttendanceDrop] != 0 {
		t.Fatal("improvement must not alert")
	}
}

func TestEvaluatePendingJustifications(t *testing.T) {
	g, s := group(), mat()
	s2 := mat()
	s2.SubjectCode, s2.SubjectEnrollmentID = "FIS1", uuid.New()
	old := base.Add(-48 * time.Hour)
	s.Requests = []RequestFact{
		{RequestID: uuid.New(), RecordID: uuid.New(), Status: ReqPendingTutor, CreatedAt: base},
		{RequestID: uuid.New(), RecordID: uuid.New(), Status: ReqTutorRejected, CreatedAt: old.Add(-time.Hour)},
	}
	s2.Requests = []RequestFact{{RequestID: uuid.New(), RecordID: uuid.New(), Status: ReqPendingTeacher, CreatedAt: old}}
	got := Evaluate(g, student(s, s2), DefaultRules())
	if len(got) != 1 || got[0].Rule != RulePendingJustifs || got[0].SubjectEnrollmentID != nil {
		t.Fatalf("candidates: %+v", got)
	}
	if got[0].Evidence["pending_tutor"] != 1 || got[0].Evidence["pending_teacher"] != 1 || got[0].Evidence["oldest_pending_request_date"] != "2026-01-31" {
		t.Fatalf("evidence: %v", got[0].Evidence)
	}
	if !strings.Contains(got[0].Message, "1 solicitudes de justificación pendientes del Tutor y 1 pendientes del Profesor") {
		t.Fatalf("message: %s", got[0].Message)
	}
	// Rejected or approved requests are not pending.
	s.Requests[0].Status, s2.Requests[0].Status = ReqJustified, ReqTeacherRejected
	if len(Evaluate(g, student(s, s2), DefaultRules())) != 0 {
		t.Fatal("finished requests must not alert")
	}
}

func TestEvaluateOnlyActiveEnrollments(t *testing.T) {
	g, s := group(), mat()
	s.Units[0].Hundredths = h32(100)
	st := student(s)
	st.EnrollmentStatus = "WITHDRAWN"
	if len(Evaluate(g, st, DefaultRules())) != 0 {
		t.Fatal("a withdrawn enrollment produces no alerts")
	}
	s.Status = "WITHDRAWN"
	if len(Evaluate(g, student(s), DefaultRules())) != 0 {
		t.Fatal("a withdrawn subject enrollment produces no alerts")
	}
}

func TestAnalyzeStudentTotalsDoNotDoubleCount(t *testing.T) {
	g := group()
	a, b := mat(), mat()
	a.LatesPerAbsence, b.LatesPerAbsence = three(), three()
	a.Records = recs("LATE", "LATE", "LATE", "ABSENT")
	b.SubjectCode, b.Records = "FIS1", recs("LATE", "LATE", "PRESENT")
	b.SubjectEnrollmentID, b.AssignmentID = uuid.New(), uuid.New()
	a.Units[0].Hundredths = h32(5000)
	sa := Analyze(g, student(a, b), DefaultRules())
	// 3 lates in MAT1 are 1 absence; 2 lates in FIS1 are none: they are never pooled (3 + 2 = 5 would be 1, same here,
	// but 2 + 2 pooled would be 1 while per subject it is 0).
	if sa.Attendance.Late != 5 || sa.Attendance.Absent != 1 || sa.Attendance.AbsencesFromLates != 1 || sa.Attendance.EffectiveAbsences != 2 {
		t.Fatalf("totals: %+v", sa.Attendance)
	}
	if sa.Grades.Total != 4 || sa.Grades.Pending != 3 || sa.Grades.NA != 1 {
		t.Fatalf("grades: %+v", sa.Grades)
	}
	b.Status = "WITHDRAWN"
	sa = Analyze(g, student(a, b), DefaultRules())
	if sa.Attendance.Late != 3 || len(sa.Subjects) != 2 {
		t.Fatalf("withdrawn subject must stay listed but not add to totals: %+v", sa.Attendance)
	}
}

func TestAnalyzeGroupSumsNumeratorsAndDenominators(t *testing.T) {
	g := group()
	asg := uuid.New()
	mk := func(records ...string) StudentFacts {
		s := mat()
		s.AssignmentID = asg
		s.Records = recs(records...)
		return student(s)
	}
	s1 := mk("PRESENT", "PRESENT") // 100%
	s2 := mk("PRESENT", "ABSENT", "ABSENT", "ABSENT")
	s2.FollowUps = FollowUpFacts{Count: 2}
	s3 := mk()
	s3.EnrollmentStatus = "WITHDRAWN"
	ga := AnalyzeGroup(g, []StudentFacts{s1, s2, s3}, DefaultRules())
	if ga.Students != 2 || len(ga.Rows) != 2 || len(ga.ByAssignment) != 1 {
		t.Fatalf("group: %d students %d rows %d assignments", ga.Students, len(ga.Rows), len(ga.ByAssignment))
	}
	// 3 attended of 6 recorded = 50%, not the average of 100% and 25% (62.5%).
	if r := ga.Attendance.Rate(); r.Num != 3 || r.Den != 6 {
		t.Fatalf("group rate = %+v", r)
	}
	if ga.StudentsWithFollowUp != 1 || ga.FollowUps != 2 || ga.SignalsByRule[RuleUnjustifiedAbsences] != 1 {
		t.Fatalf("group: %+v", ga)
	}
	if ga.ByAssignment[0].Students != 2 || ga.ByAssignment[0].Attendance.Absent != 3 {
		t.Fatalf("assignment: %+v", ga.ByAssignment[0])
	}
	// A group without records: no rate.
	empty := AnalyzeGroup(g, []StudentFacts{mk()}, DefaultRules())
	if _, ok := empty.Attendance.Rate().Hundredths(); ok {
		t.Fatal("a group without attendance records has no percentage")
	}
}

func TestRulesValidation(t *testing.T) {
	if !DefaultRules().Valid() {
		t.Fatal("default rules must be valid")
	}
	for _, r := range []Rules{{0, 5, 20}, {3, 0, 20}, {3, 5, 0}, {3, 5, 101}, {3, 51, 20}} {
		if r.Valid() {
			t.Errorf("%+v must be invalid", r)
		}
	}
	for _, rule := range []Rule{RuleGradeNA, RuleUnjustifiedAbsences, RuleAttendanceDrop, RulePendingJustifs} {
		if !rule.Valid() || rule.Describe() == "" {
			t.Errorf("rule %s lacks a description", rule)
		}
	}
	if Rule("RISK_SCORE").Valid() {
		t.Fatal("unknown rule accepted")
	}
}
