// Package http is the HTTP driving adapter of the academic module. It only
// authenticates, decodes and encodes; every authorization decision is made by
// the use cases from the authenticated principal.
package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/edutrak/backend/internal/academic/application"
	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

const dateLayout = "2006-01-02"

type Handler struct {
	svc  *application.Service
	auth idhttp.Authenticator
}

func NewHandler(svc *application.Service, auth idhttp.Authenticator) *Handler {
	return &Handler{svc: svc, auth: auth}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, p iddomain.Principal)

// Register mounts the academic routes. Every route requires authentication;
// roles and relationships are checked inside the use cases.
func (h *Handler) Register(mux *http.ServeMux) {
	route := func(pattern string, fn handlerFunc) {
		mux.Handle(pattern, idhttp.Authenticate(h.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := idhttp.PrincipalFrom(r.Context())
			fn(w, r, p)
		})))
	}
	const base = "/api/academic"

	route("GET "+base+"/careers", h.listCareers)
	route("POST "+base+"/careers", h.createCareer)
	route("GET "+base+"/careers/{id}", h.getCareer)
	route("GET "+base+"/careers/{id}/curriculum", h.listCurriculum)
	route("POST "+base+"/careers/{id}/curriculum", h.addCurriculum)

	route("GET "+base+"/subjects", h.listSubjects)
	route("POST "+base+"/subjects", h.createSubject)
	route("GET "+base+"/subjects/{id}", h.getSubject)
	route("GET "+base+"/subjects/{id}/units", h.listUnits)
	route("POST "+base+"/subjects/{id}/units", h.createUnit)

	route("GET "+base+"/terms", h.listTerms)
	route("POST "+base+"/terms", h.createTerm)
	route("GET "+base+"/terms/{id}", h.getTerm)
	route("POST "+base+"/terms/{id}/activate", h.activateTerm)
	route("POST "+base+"/terms/{id}/close", h.closeTerm)

	route("GET "+base+"/groups", h.listGroups)
	route("POST "+base+"/groups", h.createGroup)
	route("GET "+base+"/groups/{id}", h.getGroup)
	route("PUT "+base+"/groups/{id}/tutor", h.setTutor)
	route("GET "+base+"/groups/{id}/enrollments", h.listEnrollments)
	route("POST "+base+"/groups/{id}/enrollments", h.enrollStudent)
	route("GET "+base+"/groups/{id}/teaching-assignments", h.listGroupAssignments)
	route("POST "+base+"/groups/{id}/teaching-assignments", h.createAssignment)

	route("GET "+base+"/teaching-assignments", h.listAssignments)
	route("GET "+base+"/teaching-assignments/{id}", h.getAssignment)
	route("PUT "+base+"/teaching-assignments/{id}/teacher", h.changeTeacher)
	route("GET "+base+"/teaching-assignments/{id}/students", h.listAssignmentStudents)
	route("GET "+base+"/teaching-assignments/{id}/schedules", h.listAssignmentSchedules)
	route("POST "+base+"/teaching-assignments/{id}/schedules", h.createSchedule)
	route("POST "+base+"/teaching-assignments/{id}/subject-enrollments", h.enrollGroupInAssignment)

	route("GET "+base+"/schedules", h.listSchedules)
	route("GET "+base+"/subject-enrollments", h.listSubjectEnrollments)
	route("POST "+base+"/subject-enrollments", h.createSubjectEnrollment)
}

// --- helpers -------------------------------------------------------------------

func pathID(r *http.Request, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, notFound // same answer as an unknown id
	}
	return id, nil
}

func queryUUID(r *http.Request, name string) (*uuid.UUID, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, apperr.New(apperr.KindValidation, "invalid_"+name, name+" must be a UUID")
	}
	return &id, nil
}

func parseDate(field, v string) (time.Time, error) {
	t, err := time.Parse(dateLayout, v)
	if err != nil {
		return time.Time{}, apperr.New(apperr.KindValidation, "invalid_"+field, field+" must be a date in YYYY-MM-DD format")
	}
	return t, nil
}

// reply writes the success value or maps err.
func reply[T any](w http.ResponseWriter, r *http.Request, status int, v T, err error) {
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, status, v)
}

func list[T, D any](w http.ResponseWriter, r *http.Request, items []T, err error, conv func(T) D) {
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	out := make([]D, len(items))
	for i, it := range items {
		out[i] = conv(it)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// --- DTOs ------------------------------------------------------------------------

type careerDTO struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	IsActive bool      `json:"is_active"`
}

func toCareer(c domain.Career) careerDTO { return careerDTO{c.ID, c.Code, c.Name, c.IsActive} }

type subjectDTO = careerDTO

func toSubject(s domain.Subject) subjectDTO { return subjectDTO{s.ID, s.Code, s.Name, s.IsActive} }

type unitDTO struct {
	ID         uuid.UUID `json:"id"`
	SubjectID  uuid.UUID `json:"subject_id"`
	UnitNumber int16     `json:"unit_number"`
	Name       string    `json:"name"`
}

func toUnit(u domain.SubjectUnit) unitDTO { return unitDTO{u.ID, u.SubjectID, u.Number, u.Name} }

type curriculumDTO struct {
	CareerID    uuid.UUID `json:"career_id"`
	SubjectID   uuid.UUID `json:"subject_id"`
	Level       int16     `json:"level"`
	SubjectCode string    `json:"subject_code"`
	SubjectName string    `json:"subject_name"`
}

func toCurriculum(e domain.CurriculumEntry) curriculumDTO {
	return curriculumDTO{e.CareerID, e.SubjectID, e.Level, e.SubjectCode, e.SubjectName}
}

type termDTO struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	StartDate string    `json:"start_date"`
	EndDate   string    `json:"end_date"`
	Status    string    `json:"status"`
}

func toTerm(t domain.Term) termDTO {
	return termDTO{t.ID, t.Name, t.StartDate.Format(dateLayout), t.EndDate.Format(dateLayout), string(t.Status)}
}

type groupDTO struct {
	ID         uuid.UUID  `json:"id"`
	TermID     uuid.UUID  `json:"academic_term_id"`
	TermName   string     `json:"academic_term_name"`
	CareerID   uuid.UUID  `json:"career_id"`
	CareerCode string     `json:"career_code"`
	CareerName string     `json:"career_name"`
	TutorID    *uuid.UUID `json:"tutor_id"`
	Code       string     `json:"code"`
	Level      int16      `json:"level"`
}

func toGroup(g domain.Group) groupDTO {
	return groupDTO{g.ID, g.TermID, g.TermName, g.CareerID, g.CareerCode, g.CareerName, g.TutorID, g.Code, g.Level}
}

type personDTO struct {
	ID                  uuid.UUID `json:"id"`
	InstitutionalNumber string    `json:"institutional_number"`
	FirstName           string    `json:"first_name"`
	LastName            string    `json:"last_name"`
}

func toPerson(p domain.Person) personDTO {
	return personDTO{p.ID, p.InstitutionalNumber, p.FirstName, p.LastName}
}

type enrollmentDTO struct {
	ID         uuid.UUID `json:"id"`
	GroupID    uuid.UUID `json:"group_id"`
	StudentID  uuid.UUID `json:"student_id"`
	Status     string    `json:"status"`
	EnrolledAt time.Time `json:"enrolled_at"`
	Student    personDTO `json:"student"`
}

func toEnrollment(e domain.Enrollment) enrollmentDTO {
	return enrollmentDTO{e.ID, e.GroupID, e.StudentID, string(e.Status), e.EnrolledAt, toPerson(e.Student)}
}

type assignmentDTO struct {
	ID          uuid.UUID `json:"id"`
	GroupID     uuid.UUID `json:"group_id"`
	GroupCode   string    `json:"group_code"`
	SubjectID   uuid.UUID `json:"subject_id"`
	SubjectCode string    `json:"subject_code"`
	SubjectName string    `json:"subject_name"`
	TeacherID   uuid.UUID `json:"teacher_id"`
	TeacherName string    `json:"teacher_name"`
}

func toAssignment(a domain.TeachingAssignment) assignmentDTO {
	return assignmentDTO{a.ID, a.GroupID, a.GroupCode, a.SubjectID, a.SubjectCode, a.SubjectName, a.TeacherID, a.TeacherName}
}

type scheduleDTO struct {
	ID                   uuid.UUID `json:"id"`
	TeachingAssignmentID uuid.UUID `json:"teaching_assignment_id"`
	DayOfWeek            int16     `json:"day_of_week"`
	StartTime            string    `json:"start_time"`
	EndTime              string    `json:"end_time"`
	Classroom            *string   `json:"classroom"`
}

func toSchedule(s domain.Schedule) scheduleDTO {
	d := scheduleDTO{s.ID, s.TeachingAssignmentID, s.DayOfWeek, s.StartTime, s.EndTime, nil}
	if s.Classroom != "" {
		d.Classroom = &s.Classroom
	}
	return d
}

type scheduleEntryDTO struct {
	scheduleDTO
	GroupID     uuid.UUID `json:"group_id"`
	GroupCode   string    `json:"group_code"`
	SubjectCode string    `json:"subject_code"`
	SubjectName string    `json:"subject_name"`
	TeacherName string    `json:"teacher_name"`
}

func toScheduleEntry(e domain.ScheduleEntry) scheduleEntryDTO {
	a := e.Assignment
	return scheduleEntryDTO{toSchedule(e.Schedule), a.GroupID, a.GroupCode, a.SubjectCode, a.SubjectName, a.TeacherName}
}

type subjectEnrollmentDTO struct {
	ID                   uuid.UUID `json:"id"`
	StudentID            uuid.UUID `json:"student_id"`
	TeachingAssignmentID uuid.UUID `json:"teaching_assignment_id"`
	AttemptNumber        int16     `json:"attempt_number"`
	Status               string    `json:"status"`
	Student              personDTO `json:"student"`
}

func toSubjectEnrollment(e domain.SubjectEnrollment) subjectEnrollmentDTO {
	return subjectEnrollmentDTO{e.ID, e.StudentID, e.TeachingAssignmentID, e.AttemptNumber, string(e.Status), toPerson(e.Student)}
}

// --- catalogs ----------------------------------------------------------------------

func (h *Handler) listCareers(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	items, err := h.svc.ListCareers(r.Context())
	list(w, r, items, err, toCareer)
}

func (h *Handler) getCareer(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	id, err := pathID(r, domain.ErrCareerNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	c, err := h.svc.GetCareer(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCareer(*c))
}

type codeNameReq struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (h *Handler) createCareer(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req codeNameReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	c, err := h.svc.CreateCareer(r.Context(), p, application.CreateCareerInput{Code: req.Code, Name: req.Name})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toCareer(*c))
}

func (h *Handler) listSubjects(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	items, err := h.svc.ListSubjects(r.Context())
	list(w, r, items, err, toSubject)
}

func (h *Handler) getSubject(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	id, err := pathID(r, domain.ErrSubjectNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s, err := h.svc.GetSubject(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSubject(*s))
}

func (h *Handler) createSubject(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req codeNameReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s, err := h.svc.CreateSubject(r.Context(), p, application.CreateSubjectInput{Code: req.Code, Name: req.Name})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSubject(*s))
}

func (h *Handler) listUnits(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	id, err := pathID(r, domain.ErrSubjectNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListUnits(r.Context(), id)
	list(w, r, items, err, toUnit)
}

func (h *Handler) createUnit(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSubjectNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		UnitNumber int    `json:"unit_number"`
		Name       string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	u, err := h.svc.CreateUnit(r.Context(), p, id, application.CreateUnitInput{Number: req.UnitNumber, Name: req.Name})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toUnit(*u))
}

func (h *Handler) listCurriculum(w http.ResponseWriter, r *http.Request, _ iddomain.Principal) {
	id, err := pathID(r, domain.ErrCareerNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var level *int16
	if raw := r.URL.Query().Get("level"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || domain.ValidateLevel(n) != nil {
			httpx.Error(w, r, domain.ErrInvalidLevel)
			return
		}
		l := int16(n)
		level = &l
	}
	items, err := h.svc.ListCurriculum(r.Context(), id, level)
	list(w, r, items, err, toCurriculum)
}

func (h *Handler) addCurriculum(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrCareerNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		SubjectID uuid.UUID `json:"subject_id"`
		Level     int       `json:"level"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.AddCurriculum(r.Context(), p, id, application.AddCurriculumInput{SubjectID: req.SubjectID, Level: req.Level})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toCurriculum(*e))
}

// --- terms -----------------------------------------------------------------------

func (h *Handler) listTerms(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	items, err := h.svc.ListTerms(r.Context(), p)
	list(w, r, items, err, toTerm)
}

func (h *Handler) getTerm(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrTermNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, err := h.svc.GetTerm(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTerm(*t))
}

func (h *Handler) createTerm(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req struct {
		Name      string `json:"name"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	start, err := parseDate("start_date", req.StartDate)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	end, err := parseDate("end_date", req.EndDate)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, err := h.svc.CreateTerm(r.Context(), p, application.CreateTermInput{Name: req.Name, StartDate: start, EndDate: end})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTerm(*t))
}

func (h *Handler) termTransition(w http.ResponseWriter, r *http.Request, p iddomain.Principal,
	fn func(r *http.Request, p iddomain.Principal, id uuid.UUID) (*domain.Term, error)) {
	id, err := pathID(r, domain.ErrTermNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, err := fn(r, p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTerm(*t))
}

func (h *Handler) activateTerm(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	h.termTransition(w, r, p, func(r *http.Request, p iddomain.Principal, id uuid.UUID) (*domain.Term, error) {
		return h.svc.ActivateTerm(r.Context(), p, id)
	})
}

func (h *Handler) closeTerm(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	h.termTransition(w, r, p, func(r *http.Request, p iddomain.Principal, id uuid.UUID) (*domain.Term, error) {
		return h.svc.CloseTerm(r.Context(), p, id)
	})
}

// --- groups and enrollments --------------------------------------------------------

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	termID, err := queryUUID(r, "academic_term_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListGroups(r.Context(), p, termID)
	list(w, r, items, err, toGroup)
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	g, err := h.svc.GetGroup(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGroup(*g))
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req struct {
		TermID   uuid.UUID  `json:"academic_term_id"`
		CareerID uuid.UUID  `json:"career_id"`
		Code     string     `json:"code"`
		Level    int        `json:"level"`
		TutorID  *uuid.UUID `json:"tutor_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	g, err := h.svc.CreateGroup(r.Context(), p, application.CreateGroupInput{
		TermID: req.TermID, CareerID: req.CareerID, Code: req.Code, Level: req.Level, TutorID: req.TutorID,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toGroup(*g))
}

func (h *Handler) setTutor(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		TutorID json.RawMessage `json:"tutor_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(req.TutorID) == 0 {
		httpx.Error(w, r, apperr.New(apperr.KindValidation, "tutor_id_required", "tutor_id is required (use null to remove the tutor)"))
		return
	}
	var tutorID *uuid.UUID // JSON null removes the tutor
	if err := json.Unmarshal(req.TutorID, &tutorID); err != nil {
		httpx.Error(w, r, apperr.New(apperr.KindValidation, "invalid_body", "tutor_id must be a UUID or null"))
		return
	}
	g, err := h.svc.SetGroupTutor(r.Context(), p, id, tutorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGroup(*g))
}

func (h *Handler) listEnrollments(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListGroupEnrollments(r.Context(), p, id)
	list(w, r, items, err, toEnrollment)
}

func (h *Handler) enrollStudent(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		StudentID uuid.UUID `json:"student_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.EnrollStudent(r.Context(), p, id, req.StudentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toEnrollment(*e))
}

// --- teaching assignments and schedules ----------------------------------------------

func (h *Handler) listGroupAssignments(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListAssignments(r.Context(), p, &id)
	list(w, r, items, err, toAssignment)
}

func (h *Handler) listAssignments(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	groupID, err := queryUUID(r, "group_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListAssignments(r.Context(), p, groupID)
	list(w, r, items, err, toAssignment)
}

func (h *Handler) createAssignment(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGroupNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		SubjectID uuid.UUID `json:"subject_id"`
		TeacherID uuid.UUID `json:"teacher_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	a, err := h.svc.CreateAssignment(r.Context(), p, id, application.CreateAssignmentInput{SubjectID: req.SubjectID, TeacherID: req.TeacherID})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAssignment(*a))
}

func (h *Handler) getAssignment(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	a, err := h.svc.GetAssignment(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAssignment(*a))
}

func (h *Handler) changeTeacher(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		TeacherID uuid.UUID `json:"teacher_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	a, err := h.svc.ChangeTeacher(r.Context(), p, id, req.TeacherID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAssignment(*a))
}

func (h *Handler) listAssignmentStudents(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListAssignmentStudents(r.Context(), p, id)
	list(w, r, items, err, toSubjectEnrollment)
}

func (h *Handler) listAssignmentSchedules(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListAssignmentSchedules(r.Context(), p, id)
	list(w, r, items, err, toSchedule)
}

func (h *Handler) createSchedule(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		DayOfWeek int    `json:"day_of_week"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		Classroom string `json:"classroom"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s, err := h.svc.CreateSchedule(r.Context(), p, id, application.CreateScheduleInput{
		DayOfWeek: req.DayOfWeek, StartTime: req.StartTime, EndTime: req.EndTime, Classroom: req.Classroom,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSchedule(*s))
}

func (h *Handler) listSchedules(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	groupID, err := queryUUID(r, "group_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListSchedules(r.Context(), p, groupID)
	list(w, r, items, err, toScheduleEntry)
}

// --- subject enrollments ---------------------------------------------------------------

func (h *Handler) createSubjectEnrollment(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req struct {
		StudentID            uuid.UUID `json:"student_id"`
		TeachingAssignmentID uuid.UUID `json:"teaching_assignment_id"`
		AttemptNumber        *int      `json:"attempt_number"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.CreateSubjectEnrollment(r.Context(), p, application.CreateSubjectEnrollmentInput{
		StudentID: req.StudentID, TeachingAssignmentID: req.TeachingAssignmentID, AttemptNumber: req.AttemptNumber,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSubjectEnrollment(*e))
}

func (h *Handler) enrollGroupInAssignment(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	n, err := h.svc.EnrollGroupInAssignment(r.Context(), p, id)
	reply(w, r, http.StatusOK, map[string]int{"created": n}, err)
}

func (h *Handler) listSubjectEnrollments(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	assignmentID, err := queryUUID(r, "teaching_assignment_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	studentID, err := queryUUID(r, "student_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListSubjectEnrollments(r.Context(), p, ports.SubjectEnrollmentFilter{
		TeachingAssignmentID: assignmentID, StudentID: studentID,
	})
	list(w, r, items, err, toSubjectEnrollment)
}
