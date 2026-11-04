// Package http is the HTTP driving adapter of the analytics module. It authenticates,
// decodes and encodes; authorization lives in the use cases. Percentages are rendered with
// exactly two decimals and are null (never 0) when there is no denominator.
package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/edutrak/backend/internal/analytics/application"
	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

type Handler struct {
	svc  *application.Service
	auth idhttp.Authenticator
}

func NewHandler(svc *application.Service, auth idhttp.Authenticator) *Handler {
	return &Handler{svc: svc, auth: auth}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, p iddomain.Principal)

func (h *Handler) Register(mux *http.ServeMux) {
	route := func(pattern string, fn handlerFunc) {
		mux.Handle(pattern, idhttp.Authenticate(h.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := idhttp.PrincipalFrom(r.Context())
			fn(w, r, p)
		})))
	}
	const base = "/api/analytics"
	route("GET "+base+"/students/{id}", h.student)
	route("GET "+base+"/groups/{id}", h.group)
	route("GET "+base+"/overview", h.overview)
	route("GET "+base+"/alerts", h.alerts)
	route("POST "+base+"/alerts/recalculate", h.recalculate) // literal segment wins over {id}
	route("GET "+base+"/alerts/{id}", h.alert)
}

// --- query helpers ---------------------------------------------------------------------------------

func optUUID(r *http.Request, name string) (*uuid.UUID, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, domain.ErrInvalidFilter
	}
	return &id, nil
}

func page(r *http.Request) (ports.Page, error) {
	var p ports.Page
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n == 0 {
			return p, domain.ErrInvalidPaging
		}
		p.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, domain.ErrInvalidPaging
		}
		p.Offset = n
	}
	return p, nil
}

func pathID(r *http.Request, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, notFound
	}
	return id, nil
}

// --- DTO helpers -----------------------------------------------------------------------------------------

type obj = map[string]any

func pct(r domain.Ratio) any {
	h, ok := r.Hundredths()
	if !ok {
		return nil
	}
	return json.Number(domain.FormatHundredths(h))
}

func date(t time.Time) string { return t.Format("2006-01-02") }

func datePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return date(*t)
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func groupDTO(g domain.GroupInfo) obj {
	return obj{
		"group":         obj{"id": g.ID, "code": g.Code, "level": g.Level},
		"career":        obj{"id": g.CareerID, "code": g.CareerCode, "name": g.CareerName},
		"academic_term": obj{"id": g.TermID, "name": g.TermName, "status": g.TermStatus, "start_date": date(g.TermStart), "end_date": date(g.TermEnd)},
	}
}

func gradesDTO(g domain.GradeStats) obj {
	return obj{"units_total": g.Total, "units_graded": g.Graded, "units_pending": g.Pending,
		"na": g.NA, "sa": g.SA, "de": g.DE, "au": g.AU}
}

func attendanceDTO(a domain.AttendanceStats, fromLates, effective int) obj {
	out := obj{
		"closed_session_records": a.Considered(), "present": a.Present, "late": a.Late, "absent": a.Absent, "justified": a.Justified,
		"unjustified_absences": a.Absent, "attendance_pct": pct(a.Rate()),
		"absences_from_lates": fromLates, "effective_absences": effective,
	}
	if a.LatesPerAbsence != nil {
		out["lates_per_absence"] = *a.LatesPerAbsence
	} else {
		out["lates_per_absence"] = nil
	}
	return out
}

func requestsDTO(r domain.RequestStats) obj {
	return obj{
		"requests_total": r.Total, "pending_tutor": r.PendingTutor, "pending_teacher": r.PendingTeacher,
		"tutor_rejected": r.TutorRejected, "teacher_rejected": r.TeacherRejected, "approved": r.Approved,
		"absences_with_request": r.AbsencesWithRequest, "oldest_pending_request_date": datePtr(r.OldestPending),
	}
}

func windowDTO(a domain.AttendanceStats, h int) obj {
	return obj{"present": a.Present, "late": a.Late, "absent": a.Absent, "justified": a.Justified, "attendance_pct": json.Number(domain.FormatHundredths(h))}
}

func signalDTO(c domain.Candidate) obj {
	return obj{"rule": c.Rule, "rule_description": c.Rule.Describe(), "message": c.Message, "evidence": c.Evidence}
}

type alertDTO struct {
	ID          uuid.UUID       `json:"id"`
	Rule        domain.Rule     `json:"rule"`
	Description string          `json:"rule_description"`
	Status      string          `json:"status"`
	Message     string          `json:"message"`
	Evidence    json.RawMessage `json:"evidence"`
	Student     obj             `json:"student"`
	Group       obj             `json:"group"`
	Term        obj             `json:"academic_term"`
	Subject     any             `json:"subject"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	EvaluatedAt time.Time       `json:"evaluated_at"`
	ResolvedAt  any             `json:"resolved_at"`
}

func toAlert(a domain.AlertView) alertDTO {
	var subject any
	if a.SubjectEnrollmentID != nil {
		subject = obj{"subject_enrollment_id": *a.SubjectEnrollmentID, "code": a.SubjectCode, "name": a.SubjectName}
	}
	ev := json.RawMessage(a.Evidence)
	if len(ev) == 0 {
		ev = json.RawMessage(`{}`)
	}
	return alertDTO{
		ID: a.ID, Rule: a.Rule, Description: a.Rule.Describe(), Status: string(a.Status), Message: a.Message, Evidence: ev,
		Student: obj{"id": a.StudentID, "institutional_number": a.StudentNumber, "first_name": a.StudentFirst, "last_name": a.StudentLast},
		Group:   obj{"id": a.GroupID, "code": a.GroupCode}, Term: obj{"id": a.TermID, "name": a.TermName}, Subject: subject,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, EvaluatedAt: a.EvaluatedAt, ResolvedAt: timePtr(a.ResolvedAt),
	}
}

func toAlerts(list []domain.AlertView) []alertDTO {
	out := make([]alertDTO, len(list))
	for i, a := range list {
		out[i] = toAlert(a)
	}
	return out
}

func ruleCounts(m map[domain.Rule]int) obj {
	out := obj{}
	for _, r := range []domain.Rule{domain.RuleGradeNA, domain.RuleUnjustifiedAbsences, domain.RuleAttendanceDrop, domain.RulePendingJustifs} {
		out[string(r)] = m[r]
	}
	return out
}

// --- handlers --------------------------------------------------------------------------------------------

func (h *Handler) student(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrStudentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	group, err := optUUID(r, "group_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	v, err := h.svc.GetStudentAnalytics(r.Context(), p, id, group)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	a := v.Analytics
	subjects := make([]obj, len(a.Subjects))
	for i, s := range a.Subjects {
		na := make([]obj, len(s.NAUnits))
		for j, u := range s.NAUnits {
			score, cat, _ := domain.UnitScore(u)
			na[j] = obj{"subject_unit_id": u.UnitID, "unit_number": u.Number, "unit_name": u.Name, "score": json.Number(score), "category": cat}
		}
		var trend any // null = not enough comparable data
		if s.Trend != nil {
			trend = obj{
				"window_sessions": s.Trend.Window, "recent": windowDTO(s.Trend.Recent, s.Trend.RecentPct), "previous": windowDTO(s.Trend.Previous, s.Trend.PreviousPct),
				"drop_points": json.Number(domain.FormatHundredths(s.Trend.DropHundredths)),
			}
		}
		grades := gradesDTO(s.Grades)
		grades["na_units"] = na
		subjects[i] = obj{
			"subject_enrollment_id": s.Facts.SubjectEnrollmentID, "teaching_assignment_id": s.Facts.AssignmentID,
			"subject": obj{"code": s.Facts.SubjectCode, "name": s.Facts.SubjectName}, "attempt_number": s.Facts.Attempt, "status": s.Facts.Status,
			"grades": grades, "attendance": attendanceDTO(s.Attendance, s.Attendance.AbsencesFromLates(), s.Attendance.EffectiveAbsences()),
			"attendance_trend": trend, "justifications": requestsDTO(s.Requests),
		}
	}
	signals := make([]obj, len(a.Signals))
	for i, c := range a.Signals {
		signals[i] = signalDTO(c)
	}
	e := v.Enrollment
	out := groupDTO(e.GroupInfo)
	out["student"] = obj{"id": a.Student.StudentID, "institutional_number": a.Student.Number, "first_name": a.Student.First, "last_name": a.Student.Last}
	out["enrollment_status"] = e.Status
	out["subjects"] = subjects
	out["totals"] = obj{
		"grades": gradesDTO(a.Grades), "attendance": attendanceDTO(a.Attendance.AttendanceStats, a.Attendance.AbsencesFromLates, a.Attendance.EffectiveAbsences),
		"justifications": requestsDTO(a.Requests),
	}
	out["tutoring"] = obj{"followups_count": a.Student.FollowUps.Count, "last_followup_date": datePtr(a.Student.FollowUps.Last)}
	out["signals"] = signals
	out["alerts"] = toAlerts(v.Alerts)
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) group(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	v, err := h.svc.GetGroupAnalytics(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	assignments := make([]obj, len(v.ByAssignment))
	for i, a := range v.ByAssignment {
		assignments[i] = obj{
			"teaching_assignment_id": a.AssignmentID, "subject": obj{"code": a.SubjectCode, "name": a.SubjectName}, "active_subject_enrollments": a.Students,
			"grades": gradesDTO(a.Grades), "attendance": attendanceDTO(a.Attendance, a.Attendance.AbsencesFromLates(), a.Attendance.EffectiveAbsences()),
			"justifications": requestsDTO(a.Requests),
		}
	}
	rows := make([]obj, len(v.Rows))
	for i, s := range v.Rows {
		rows[i] = obj{
			"student":        obj{"id": s.StudentID, "institutional_number": s.Number, "first_name": s.First, "last_name": s.Last},
			"attendance_pct": pct(s.AttendanceRate), "unjustified_absences": s.UnjustifiedAbsences, "effective_absences": s.EffectiveAbs,
			"na_units": s.NA, "pending_units": s.PendingUnits, "pending_justification_requests": s.PendingRequests,
			"followups_count": s.FollowUps, "signals": s.Signal,
		}
	}
	signals := obj{}
	for _, rule := range []domain.Rule{domain.RuleGradeNA, domain.RuleUnjustifiedAbsences, domain.RuleAttendanceDrop, domain.RulePendingJustifs} {
		signals[string(rule)] = v.SignalsByRule[rule]
	}
	out := groupDTO(v.Group)
	out["active_students"] = v.Students
	out["grades"] = gradesDTO(v.Grades)
	out["attendance"] = attendanceDTO(v.Attendance.AttendanceStats, v.Attendance.AbsencesFromLates, v.Attendance.EffectiveAbsences)
	out["justifications"] = requestsDTO(v.Requests)
	out["tutoring"] = obj{"students_with_followup": v.StudentsWithFollowUp, "students_without_followup": v.Students - v.StudentsWithFollowUp, "followups_count": v.FollowUps}
	out["signals_now"] = signals
	out["open_alerts"] = ruleCounts(v.OpenAlerts)
	out["by_assignment"] = assignments
	out["students"] = rows
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	term, err := optUUID(r, "academic_term_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	career, err := optUUID(r, "career_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list, err := h.svc.Overview(r.Context(), p, ports.OverviewFilter{TermID: term, CareerID: career})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rows := make([]obj, len(list))
	for i, o := range list {
		row := groupDTO(o.Group)
		row["active_students"] = o.ActiveStudents
		row["grades"] = gradesDTO(o.Grades)
		row["attendance"] = obj{
			"closed_session_records": o.Attendance.Considered(), "present": o.Attendance.Present, "late": o.Attendance.Late,
			"absent": o.Attendance.Absent, "justified": o.Attendance.Justified, "unjustified_absences": o.Attendance.Absent,
			"attendance_pct": pct(o.Attendance.Rate()),
		}
		row["justifications"] = obj{
			"requests_total": o.Requests.Total, "pending_tutor": o.Requests.PendingTutor, "pending_teacher": o.Requests.PendingTeacher,
			"tutor_rejected": o.Requests.TutorRejected, "teacher_rejected": o.Requests.TeacherRejected, "approved": o.Requests.Approved,
			"absences_with_request": o.Requests.AbsencesWithRequest,
		}
		row["tutoring"] = obj{"students_with_followup": o.StudentsWithFollowUp, "students_without_followup": o.ActiveStudents - o.StudentsWithFollowUp}
		row["open_alerts"] = o.OpenAlerts
		rows[i] = row
	}
	httpx.JSON(w, http.StatusOK, obj{"data": rows})
}

func (h *Handler) alerts(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var f ports.AlertFilter
	var err error
	for _, step := range []func() error{
		func() (e error) { f.StudentID, e = optUUID(r, "student_id"); return },
		func() (e error) { f.GroupID, e = optUUID(r, "group_id"); return },
		func() (e error) { f.TermID, e = optUUID(r, "academic_term_id"); return },
	} {
		if err = step(); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	f.Rule, f.Status = r.URL.Query().Get("type"), r.URL.Query().Get("status")
	pg, err := page(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list, total, err := h.svc.ListAlerts(r.Context(), p, f, pg)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if pg.Limit == 0 {
		pg.Limit = application.DefaultLimit
	}
	httpx.JSON(w, http.StatusOK, obj{"data": toAlerts(list), "total": total, "limit": pg.Limit, "offset": pg.Offset})
}

func (h *Handler) alert(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAlertNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	a, err := h.svc.GetAlert(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAlert(*a))
}

type recalcBody struct {
	GroupID   string  `json:"group_id"`
	StudentID *string `json:"student_id"`
}

func (h *Handler) recalculate(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var b recalcBody
	if err := httpx.DecodeJSON(w, r, &b); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if b.GroupID == "" {
		httpx.Error(w, r, domain.ErrGroupRequired)
		return
	}
	group, err := uuid.Parse(b.GroupID)
	if err != nil {
		httpx.Error(w, r, domain.ErrInvalidGroup)
		return
	}
	var student *uuid.UUID
	if b.StudentID != nil {
		id, err := uuid.Parse(*b.StudentID)
		if err != nil {
			httpx.Error(w, r, domain.ErrInvalidStudent)
			return
		}
		student = &id
	}
	res, err := h.svc.RecalculateAlerts(r.Context(), p, group, student)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, obj{
		"group_id": res.GroupID, "student_id": res.StudentID, "evaluated_at": res.EvaluatedAt,
		"created": res.Created, "updated": res.Updated, "resolved": res.Resolved, "unchanged": res.Unchanged,
		"open_alerts": ruleCounts(res.Open),
	})
}
