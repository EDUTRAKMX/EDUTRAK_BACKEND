// Package http is the HTTP driving adapter of the grades module. It only
// authenticates, decodes and encodes; authorization is decided by the use cases.
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/edutrak/backend/internal/grades/application"
	"github.com/edutrak/backend/internal/grades/domain"
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
	route("POST /api/grades", h.capture)
	route("GET /api/grades/{id}", h.get)
	route("PATCH /api/grades/{id}", h.update)
	route("GET /api/students/{id}/grades", h.studentGrades)
	route("GET /api/teaching-assignments/{id}/grades", h.assignmentGrades)
}

func pathID(r *http.Request, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, notFound
	}
	return id, nil
}

// --- DTOs ------------------------------------------------------------------------------

// score is serialized as a JSON number with exactly two decimals ("90.00" -> 90.00),
// never through float64, so what was captured is what is read back.
func scoreJSON(s domain.Score) json.Number { return json.Number(s.String()) }

type gradeDTO struct {
	ID                  uuid.UUID   `json:"id"`
	SubjectEnrollmentID uuid.UUID   `json:"subject_enrollment_id"`
	SubjectUnitID       uuid.UUID   `json:"subject_unit_id"`
	Score               json.Number `json:"score"`
	Category            string      `json:"category"`
	RecordedBy          uuid.UUID   `json:"recorded_by"`
	RecordedAt          time.Time   `json:"recorded_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
}

func toGrade(g domain.Grade) gradeDTO {
	return gradeDTO{g.ID, g.SubjectEnrollmentID, g.SubjectUnitID, scoreJSON(g.Score), string(g.Score.Category()), g.RecordedBy, g.RecordedAt, g.UpdatedAt}
}

// unitGradeDTO: grade is null while the unit is pending (a pending unit is NOT 0.00).
type unitGradeDTO struct {
	SubjectUnitID uuid.UUID `json:"subject_unit_id"`
	UnitNumber    int16     `json:"unit_number"`
	UnitName      string    `json:"unit_name"`
	Grade         *struct {
		ID        uuid.UUID   `json:"id"`
		Score     json.Number `json:"score"`
		Category  string      `json:"category"`
		UpdatedAt time.Time   `json:"updated_at"`
	} `json:"grade"`
}

func toUnitGrades(units []domain.UnitGrade) []unitGradeDTO {
	out := make([]unitGradeDTO, len(units))
	for i, u := range units {
		d := unitGradeDTO{SubjectUnitID: u.Unit.ID, UnitNumber: u.Unit.Number, UnitName: u.Unit.Name}
		if u.Score != nil && u.GradeID != nil && u.UpdatedAt != nil {
			d.Grade = &struct {
				ID        uuid.UUID   `json:"id"`
				Score     json.Number `json:"score"`
				Category  string      `json:"category"`
				UpdatedAt time.Time   `json:"updated_at"`
			}{*u.GradeID, scoreJSON(*u.Score), string(u.Score.Category()), *u.UpdatedAt}
		}
		out[i] = d
	}
	return out
}

// --- handlers -----------------------------------------------------------------------------

func (h *Handler) capture(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req struct {
		SubjectEnrollmentID uuid.UUID   `json:"subject_enrollment_id"`
		SubjectUnitID       uuid.UUID   `json:"subject_unit_id"`
		Score               json.Number `json:"score"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	g, err := h.svc.CaptureGrade(r.Context(), p, application.CaptureGradeInput{
		SubjectEnrollmentID: req.SubjectEnrollmentID, SubjectUnitID: req.SubjectUnitID, Score: req.Score.String(),
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toGrade(*g))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGradeNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	g, err := h.svc.GetGrade(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGrade(*g))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrGradeNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		Score json.Number `json:"score"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	g, err := h.svc.UpdateGrade(r.Context(), p, id, req.Score.String())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGrade(*g))
}

func (h *Handler) studentGrades(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrStudentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sheets, err := h.svc.ListStudentGrades(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type subject struct {
		ID   uuid.UUID `json:"id"`
		Code string    `json:"code"`
		Name string    `json:"name"`
	}
	type term struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	type sheetDTO struct {
		SubjectEnrollmentID  uuid.UUID      `json:"subject_enrollment_id"`
		TeachingAssignmentID uuid.UUID      `json:"teaching_assignment_id"`
		AttemptNumber        int16          `json:"attempt_number"`
		Status               string         `json:"status"`
		Term                 term           `json:"academic_term"`
		GroupCode            string         `json:"group_code"`
		Subject              subject        `json:"subject"`
		Units                []unitGradeDTO `json:"units"`
	}
	out := make([]sheetDTO, len(sheets))
	for i, s := range sheets {
		out[i] = sheetDTO{s.SubjectEnrollmentID, s.AssignmentID, s.AttemptNumber, s.Status, term{s.TermID, s.TermName},
			s.GroupCode, subject{s.SubjectID, s.SubjectCode, s.SubjectName}, toUnitGrades(s.Units)}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"student_id": id, "data": out})
}

func (h *Handler) assignmentGrades(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sh, err := h.svc.GetAssignmentGrades(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type unitDTO struct {
		ID     uuid.UUID `json:"id"`
		Number int16     `json:"unit_number"`
		Name   string    `json:"name"`
	}
	type studentDTO struct {
		ID                  uuid.UUID `json:"id"`
		InstitutionalNumber string    `json:"institutional_number"`
		FirstName           string    `json:"first_name"`
		LastName            string    `json:"last_name"`
	}
	type rowDTO struct {
		SubjectEnrollmentID uuid.UUID      `json:"subject_enrollment_id"`
		Student             studentDTO     `json:"student"`
		AttemptNumber       int16          `json:"attempt_number"`
		Status              string         `json:"status"`
		Units               []unitGradeDTO `json:"units"`
	}
	units := make([]unitDTO, len(sh.Units))
	for i, u := range sh.Units {
		units[i] = unitDTO{u.ID, u.Number, u.Name}
	}
	rows := make([]rowDTO, len(sh.Rows))
	for i, r := range sh.Rows {
		rows[i] = rowDTO{r.SubjectEnrollmentID, studentDTO{r.Student.ID, r.Student.InstitutionalNumber, r.Student.FirstName, r.Student.LastName},
			r.AttemptNumber, r.Status, toUnitGrades(r.Units)}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"teaching_assignment_id": sh.AssignmentID, "group_id": sh.GroupID, "group_code": sh.GroupCode,
		"subject":  map[string]any{"id": sh.SubjectID, "code": sh.SubjectCode, "name": sh.SubjectName},
		"units":    units,
		"students": rows,
	})
}
