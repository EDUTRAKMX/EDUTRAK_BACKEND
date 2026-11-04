// Package http is the HTTP driving adapter of the attendance module. It
// authenticates, decodes and encodes; authorization lives in the use cases.
// It is also the only place that derives the client address, and it does so
// from the real connection (or a trusted proxy chain), never from the request body.
package http

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/edutrak/backend/internal/attendance/application"
	"github.com/edutrak/backend/internal/attendance/domain"
	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/edutrak/backend/internal/platform/netx"
	"github.com/google/uuid"
)

const dateLayout = "2006-01-02"

type Handler struct {
	svc            *application.Service
	auth           idhttp.Authenticator
	trustedProxies []netip.Prefix
}

func NewHandler(svc *application.Service, auth idhttp.Authenticator, trustedProxies []netip.Prefix) *Handler {
	return &Handler{svc: svc, auth: auth, trustedProxies: trustedProxies}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, p iddomain.Principal)

func (h *Handler) Register(mux *http.ServeMux) {
	route := func(pattern string, fn handlerFunc) {
		mux.Handle(pattern, idhttp.Authenticate(h.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := idhttp.PrincipalFrom(r.Context())
			fn(w, r, p)
		})))
	}
	const base = "/api/attendance"
	route("POST "+base+"/sessions", h.start)
	route("GET "+base+"/sessions/{id}", h.getSession)
	route("POST "+base+"/sessions/{id}/qr/rotate", h.rotateQR)
	route("POST "+base+"/sessions/{id}/check-in", h.checkIn)
	route("POST "+base+"/sessions/{id}/manual", h.manual)
	route("POST "+base+"/sessions/{id}/close", h.closeSession)
	route("PATCH "+base+"/records/{id}", h.correct)
	route("GET "+base+"/teaching-assignments/{id}", h.assignmentOverview)
	route("GET "+base+"/students/{id}", h.studentAttendance)
}

func pathID(r *http.Request, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, notFound
	}
	return id, nil
}

// clientIP is the address of the real peer, or of the client behind a TRUSTED
// proxy chain. The zero Addr (unknown) is denied by the network policy.
func (h *Handler) clientIP(r *http.Request) netip.Addr {
	if ip, ok := netx.ClientIP(r, h.trustedProxies); ok {
		return ip
	}
	return netip.Addr{}
}

// --- DTOs ---------------------------------------------------------------------------------

type sessionDTO struct {
	ID               uuid.UUID  `json:"id"`
	AssignmentID     uuid.UUID  `json:"teaching_assignment_id"`
	ScheduleID       *uuid.UUID `json:"schedule_id"`
	SessionDate      string     `json:"session_date"`
	Status           string     `json:"status"`
	OpenedBy         uuid.UUID  `json:"opened_by"`
	OpenedAt         time.Time  `json:"opened_at"`
	ClosedAt         *time.Time `json:"closed_at"`
	LateAfterMinutes int        `json:"late_after_minutes"`
	LatesPerAbsence  int        `json:"lates_per_absence"`
}

// The QR hash and expiry are never part of a session payload.
func toSession(s domain.Session) sessionDTO {
	return sessionDTO{s.ID, s.AssignmentID, s.ScheduleID, s.Date.Format(dateLayout), string(s.Status), s.OpenedBy, s.OpenedAt, s.ClosedAt, s.LateAfterMinutes, s.LatesPerAbsence}
}

type recordDTO struct {
	ID                  uuid.UUID `json:"id"`
	SessionID           uuid.UUID `json:"attendance_session_id"`
	SubjectEnrollmentID uuid.UUID `json:"subject_enrollment_id"`
	Status              string    `json:"status"`
	Method              string    `json:"method"`
	RecordedAt          time.Time `json:"recorded_at"`
	RecordedBy          uuid.UUID `json:"recorded_by"`
}

func toRecord(r domain.Record) recordDTO {
	return recordDTO{r.ID, r.SessionID, r.SubjectEnrollmentID, string(r.Status), string(r.Method), r.RecordedAt, r.RecordedBy}
}

type countsDTO struct {
	Present   int `json:"present"`
	Late      int `json:"late"`
	Absent    int `json:"absent"`
	Justified int `json:"justified"`
}

func toCounts(c domain.Counts) countsDTO { return countsDTO{c.Present, c.Late, c.Absent, c.Justified} }

// indicatorsDTO carries the "N lates = 1 absence" equivalence as a computed
// indicator; LATE records themselves are never rewritten.
type indicatorsDTO struct {
	countsDTO
	LatesPerAbsence   int `json:"lates_per_absence"`
	AbsencesFromLates int `json:"absences_from_lates"`
	EffectiveAbsences int `json:"effective_absences"`
}

func toIndicators(c domain.Counts, latesPerAbsence int) indicatorsDTO {
	return indicatorsDTO{toCounts(c), latesPerAbsence, c.AbsencesFromLates(latesPerAbsence), c.EffectiveAbsences(latesPerAbsence)}
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

// --- handlers --------------------------------------------------------------------------------

func (h *Handler) start(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	var req struct {
		AssignmentID     uuid.UUID  `json:"teaching_assignment_id"`
		ScheduleID       *uuid.UUID `json:"schedule_id"`
		LateAfterMinutes *int       `json:"late_after_minutes"`
		LatesPerAbsence  *int       `json:"lates_per_absence"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s, err := h.svc.StartSession(r.Context(), p, application.StartSessionInput{
		AssignmentID: req.AssignmentID, ScheduleID: req.ScheduleID, LateAfterMinutes: req.LateAfterMinutes, LatesPerAbsence: req.LatesPerAbsence,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSession(*s))
}

func (h *Handler) rotateQR(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSessionNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.RotateQR(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// The token is returned once and never logged; Angular renders it as a QR.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"attendance_session_id": res.SessionID, "token": res.Token, "expires_at": res.ExpiresAt,
	})
}

func (h *Handler) checkIn(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSessionNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Only the token: who is registering comes from the session, when from the
	// server clock and where from the connection. Any other field is rejected.
	var req struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, err := h.svc.CheckIn(r.Context(), p, application.CheckInInput{SessionID: id, Token: req.Token, ClientIP: h.clientIP(r)})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRecord(*rec))
}

func (h *Handler) manual(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSessionNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		SubjectEnrollmentID uuid.UUID `json:"subject_enrollment_id"`
		Status              string    `json:"status"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, err := h.svc.RegisterManually(r.Context(), p, application.ManualInput{SessionID: id, SubjectEnrollmentID: req.SubjectEnrollmentID, Status: req.Status})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRecord(*rec))
}

func (h *Handler) correct(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrRecordNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, err := h.svc.CorrectRecord(r.Context(), p, id, req.Status)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRecord(*rec))
}

func (h *Handler) closeSession(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSessionNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CloseSession(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"session": toSession(*res.Session), "absences_created": res.AbsencesCreated})
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrSessionNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := h.svc.GetSession(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type rowDTO struct {
		SubjectEnrollmentID uuid.UUID  `json:"subject_enrollment_id"`
		Student             personDTO  `json:"student"`
		Record              *recordDTO `json:"record"` // null while nothing is registered
	}
	rows := make([]rowDTO, len(d.Rows))
	for i, row := range d.Rows {
		rows[i] = rowDTO{SubjectEnrollmentID: row.SubjectEnrollmentID, Student: toPerson(row.Student)}
		if row.Record != nil {
			rec := toRecord(*row.Record)
			rows[i].Record = &rec
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"session": toSession(d.Session), "counts": toCounts(d.Counts), "pending": len(d.Rows) - d.Counts.Total(), "students": rows,
	})
}

func (h *Handler) assignmentOverview(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrAssignmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ov, err := h.svc.GetAssignmentAttendance(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type sessionSummary struct {
		sessionDTO
		Counts countsDTO `json:"counts"`
	}
	type studentSummary struct {
		SubjectEnrollmentID uuid.UUID     `json:"subject_enrollment_id"`
		Student             personDTO     `json:"student"`
		Status              string        `json:"status"`
		Attendance          indicatorsDTO `json:"attendance"`
	}
	sessions := make([]sessionSummary, len(ov.Sessions))
	for i, s := range ov.Sessions {
		sessions[i] = sessionSummary{toSession(s.Session), toCounts(s.Counts)}
	}
	students := make([]studentSummary, len(ov.Students))
	for i, st := range ov.Students {
		students[i] = studentSummary{st.SubjectEnrollmentID, toPerson(st.Student), st.Status, toIndicators(st.Counts, ov.LatesPerAbsence)}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"teaching_assignment_id": ov.AssignmentID, "total_sessions": len(ov.Sessions), "sessions": sessions, "students": students,
	})
}

func (h *Handler) studentAttendance(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathID(r, domain.ErrStudentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.GetStudentAttendance(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type recDTO struct {
		ID          uuid.UUID `json:"id"`
		SessionID   uuid.UUID `json:"attendance_session_id"`
		SessionDate string    `json:"session_date"`
		Status      string    `json:"status"`
		Method      string    `json:"method"`
		RecordedAt  time.Time `json:"recorded_at"`
	}
	type itemDTO struct {
		SubjectEnrollmentID  uuid.UUID      `json:"subject_enrollment_id"`
		TeachingAssignmentID uuid.UUID      `json:"teaching_assignment_id"`
		AttemptNumber        int16          `json:"attempt_number"`
		AcademicTerm         map[string]any `json:"academic_term"`
		GroupCode            string         `json:"group_code"`
		Subject              map[string]any `json:"subject"`
		Attendance           indicatorsDTO  `json:"attendance"`
		Records              []recDTO       `json:"records"`
	}
	out := make([]itemDTO, len(items))
	for i, it := range items {
		recs := make([]recDTO, len(it.Records))
		for j, rec := range it.Records {
			recs[j] = recDTO{rec.RecordID, rec.SessionID, rec.SessionDate.Format(dateLayout), string(rec.Status), string(rec.Method), rec.RecordedAt}
		}
		out[i] = itemDTO{it.SubjectEnrollmentID, it.AssignmentID, it.AttemptNumber,
			map[string]any{"id": it.TermID, "name": it.TermName}, it.GroupCode,
			map[string]any{"id": it.SubjectID, "code": it.SubjectCode, "name": it.SubjectName},
			toIndicators(it.Counts, it.LatesPerAbsence), recs}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"student_id": id, "data": out})
}
