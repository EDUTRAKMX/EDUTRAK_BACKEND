// Package http is the HTTP driving adapter of the tutoring module. It authenticates,
// decodes and encodes; authorization lives in the use cases. Follow-up notes are private:
// they are only written to the response of an authorized caller and never to logs.
package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/edutrak/backend/internal/tutoring/application"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
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
	const base = "/api/tutoring"
	route("GET "+base+"/students", h.students)
	route("GET "+base+"/students/{id}", h.overview)
	route("GET "+base+"/students/{id}/follow-ups", h.studentHistory)
	route("POST "+base+"/follow-ups", h.create)
	route("GET "+base+"/follow-ups", h.list)
	route("GET "+base+"/follow-ups/{id}", h.get)
}

// --- query helpers ---------------------------------------------------------------------------

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

func optDate(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	d, err := time.Parse(domain.DateLayout, v)
	if err != nil {
		return nil, domain.ErrInvalidFilter
	}
	return &d, nil
}

func page(r *http.Request) (ports.Page, error) {
	var p ports.Page
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, domain.ErrInvalidPaging
		}
		p.Limit = n
		if n == 0 { // 0 must not mean "default" when it was explicit
			return p, domain.ErrInvalidPaging
		}
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

// --- DTOs -----------------------------------------------------------------------------------------

type studentDTO struct {
	ID                  uuid.UUID `json:"id"`
	InstitutionalNumber string    `json:"institutional_number"`
	FirstName           string    `json:"first_name"`
	LastName            string    `json:"last_name"`
}

type enrollmentDTO struct {
	Student          studentDTO     `json:"student"`
	Group            map[string]any `json:"group"`
	Career           map[string]any `json:"career"`
	AcademicTerm     map[string]any `json:"academic_term"`
	EnrollmentStatus string         `json:"enrollment_status"`
	FollowUpsCount   int            `json:"followups_count"`
	LastFollowUpDate *string        `json:"last_followup_date"`
}

func date(t time.Time) string { return t.Format(domain.DateLayout) }

func toEnrollment(e domain.Enrollment) enrollmentDTO {
	var last *string
	if e.LastFollowUpDate != nil {
		s := date(*e.LastFollowUpDate)
		last = &s
	}
	var tutor *uuid.UUID = e.GroupTutorID
	return enrollmentDTO{
		Student: studentDTO{e.StudentID, e.StudentNumber, e.StudentFirst, e.StudentLast},
		Group:   map[string]any{"id": e.GroupID, "code": e.GroupCode, "level": e.Level, "tutor_id": tutor},
		Career:  map[string]any{"code": e.CareerCode, "name": e.CareerName},
		AcademicTerm: map[string]any{
			"id": e.TermID, "name": e.TermName, "status": e.TermStatus, "start_date": date(e.TermStart), "end_date": date(e.TermEnd),
		},
		EnrollmentStatus: e.EnrollmentStatus, FollowUpsCount: e.FollowUpCount, LastFollowUpDate: last,
	}
}

type entryDTO struct {
	ID           uuid.UUID      `json:"id"`
	Student      studentDTO     `json:"student"`
	Group        map[string]any `json:"group"`
	AcademicTerm map[string]any `json:"academic_term"`
	Author       map[string]any `json:"author"`
	FollowUpDate string         `json:"followup_date"`
	Note         string         `json:"note"`
	CreatedAt    time.Time      `json:"created_at"`
}

func toEntry(e domain.Entry) entryDTO {
	return entryDTO{
		ID:           e.ID,
		Student:      studentDTO{e.StudentID, e.StudentNumber, e.StudentFirst, e.StudentLast},
		Group:        map[string]any{"id": e.GroupID, "code": e.GroupCode},
		AcademicTerm: map[string]any{"id": e.TermID, "name": e.TermName},
		Author:       map[string]any{"id": e.TutorID, "name": e.TutorFirst + " " + e.TutorLast},
		FollowUpDate: date(e.Date), Note: e.Note, CreatedAt: e.CreatedAt,
	}
}

func toEntries(list []domain.Entry) []entryDTO {
	out := make([]entryDTO, len(list))
	for i, e := range list {
		out[i] = toEntry(e)
	}
	return out
}

// --- handlers ---------------------------------------------------------------------------------------

func (h *Handler) students(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	term, err := optUUID(r, "academic_term_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	group, err := optUUID(r, "group_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list, err := h.svc.Students(r.Context(), p, ports.StudentFilter{TermID: term, GroupID: group, Status: r.URL.Query().Get("status")})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	out := make([]enrollmentDTO, len(list))
	for i, e := range list {
		out[i] = toEnrollment(e)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
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
	ov, err := h.svc.Overview(r.Context(), p, id, group)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	subjects := make([]map[string]any, len(ov.Subjects))
	for i, s := range ov.Subjects {
		units := make([]map[string]any, len(s.Units))
		graded := 0
		for j, u := range s.Units {
			var grade any // null = not captured yet; never 0.00
			if score, cat, ok := u.Score(); ok {
				graded++
				grade = map[string]any{"score": jsonNumber(score), "category": cat}
			}
			units[j] = map[string]any{"subject_unit_id": u.UnitID, "unit_number": u.UnitNumber, "unit_name": u.UnitName, "grade": grade}
		}
		total, fromLates, effective := s.Attendance.Indicators()
		subjects[i] = map[string]any{
			"subject_enrollment_id": s.SubjectEnrollmentID, "teaching_assignment_id": s.AssignmentID,
			"subject":        map[string]any{"code": s.SubjectCode, "name": s.SubjectName},
			"teacher":        map[string]any{"name": s.TeacherFirst + " " + s.TeacherLast},
			"attempt_number": s.AttemptNumber, "status": s.Status,
			"grades": map[string]any{"units_total": len(s.Units), "units_graded": graded, "units": units},
			"attendance": map[string]any{
				"present": s.Attendance.Present, "late": s.Attendance.Late, "absent": s.Attendance.Absent, "justified": s.Attendance.Justified,
				"total": total, "lates_per_absence": s.Attendance.LatesPerAbsence,
				"absences_from_lates": fromLates, "effective_absences": effective,
			},
			"justifications": map[string]any{
				"pending": s.Justifications.Pending, "approved": s.Justifications.Approved, "rejected": s.Justifications.Rejected,
			},
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"enrollment": toEnrollment(ov.Enrollment), "subjects": subjects})
}

func (h *Handler) studentHistory(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
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
	pg, err := page(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list, total, err := h.svc.StudentHistory(r.Context(), p, id, group, pg)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	writePage(w, list, total, pg)
}

func writePage(w http.ResponseWriter, list []domain.Entry, total int, pg ports.Page) {
	if pg.Limit == 0 {
		pg.Limit = application.DefaultLimit
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": toEntries(list), "total": total, "limit": pg.Limit, "offset": pg.Offset})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var f ports.FollowUpFilter
	var err error
	for _, step := range []func() error{
		func() (e error) { f.StudentID, e = optUUID(r, "student_id"); return },
		func() (e error) { f.GroupID, e = optUUID(r, "group_id"); return },
		func() (e error) { f.TermID, e = optUUID(r, "academic_term_id"); return },
		func() (e error) { f.From, e = optDate(r, "from"); return },
		func() (e error) { f.To, e = optDate(r, "to"); return },
	} {
		if err = step(); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	pg, err := page(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list, total, err := h.svc.ListFollowUps(r.Context(), p, f, pg)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	writePage(w, list, total, pg)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrFollowUpNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.GetFollowUp(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEntry(*e))
}

type createBody struct {
	StudentID    string  `json:"student_id"`
	GroupID      string  `json:"group_id"`
	FollowUpDate *string `json:"followup_date"`
	Note         string  `json:"note"`
}

// create: the author is the authenticated user. Unknown fields (a tutor_id, for example)
// are rejected with 400 by DecodeJSON, so the client cannot even try to supply one.
func (h *Handler) create(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var b createBody
	if err := httpx.DecodeJSON(w, r, &b); err != nil {
		httpx.Error(w, r, err)
		return
	}
	student, err := uuid.Parse(b.StudentID)
	if err != nil {
		httpx.Error(w, r, domain.ErrInvalidStudentID)
		return
	}
	group, err := uuid.Parse(b.GroupID)
	if err != nil {
		httpx.Error(w, r, domain.ErrInvalidGroupID)
		return
	}
	in := application.CreateInput{StudentID: student, GroupID: group, Note: b.Note}
	if b.FollowUpDate != nil {
		d, err := domain.ParseDate(*b.FollowUpDate)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		in.Date = &d
	}
	e, err := h.svc.CreateFollowUp(r.Context(), p, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toEntry(*e))
}

// jsonNumber serializes a score with exactly two decimals, as Grades does (never via float64).
func jsonNumber(s string) json.Number { return json.Number(s) }
