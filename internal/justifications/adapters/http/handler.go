// Package http is the HTTP driving adapter of the justifications module. It
// authenticates, decodes and encodes; authorization lives in the use cases.
// Evidence travels as multipart/form-data, streamed (never base64 in JSON and never
// fully buffered), and is only served back through an authorized call.
package http

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/application"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

const (
	dateLayout    = "2006-01-02"
	maxFieldBytes = 16 << 10 // a text form field (the reason is capped far below this)
	formOverhead  = 1 << 20  // multipart framing and the text fields
)

type Handler struct {
	svc     *application.Service
	auth    idhttp.Authenticator
	maxBody int64
}

// NewHandler caps a whole request body at MaxEvidenceFiles files of maxFileBytes each plus
// some framing; each file is additionally capped by the use case.
func NewHandler(svc *application.Service, auth idhttp.Authenticator, maxFileBytes int64) *Handler {
	return &Handler{svc: svc, auth: auth, maxBody: int64(domain.MaxEvidenceFiles)*maxFileBytes + formOverhead}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, p iddomain.Principal)

func (h *Handler) Register(mux *http.ServeMux) {
	route := func(pattern string, fn handlerFunc) {
		mux.Handle(pattern, idhttp.Authenticate(h.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := idhttp.PrincipalFrom(r.Context())
			fn(w, r, p)
		})))
	}
	const base = "/api/justifications"
	route("POST "+base, h.request)
	route("GET "+base, h.list)
	route("GET "+base+"/{id}", h.get)
	route("POST "+base+"/{id}/tutor-decision", h.tutorDecision)
	route("POST "+base+"/{id}/teacher-decision", h.teacherDecision)
	route("GET "+base+"/{id}/evidence/{evidenceId}", h.downloadEvidence)
}

func pathUUID(r *http.Request, name string, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, notFound
	}
	return id, nil
}

// --- DTOs --------------------------------------------------------------------------------------------
// Nothing here carries storage keys or physical paths.

type personDTO struct {
	ID                  uuid.UUID `json:"id"`
	InstitutionalNumber string    `json:"institutional_number"`
	FirstName           string    `json:"first_name"`
	LastName            string    `json:"last_name"`
}

type caseDTO struct {
	ID                   uuid.UUID      `json:"id"`
	AttendanceRecordID   uuid.UUID      `json:"attendance_record_id"`
	TeachingAssignmentID uuid.UUID      `json:"teaching_assignment_id"`
	Status               string         `json:"status"`
	AwaitingStage        *string        `json:"awaiting_stage"` // TUTOR | TEACHER | null when finished
	Reason               string         `json:"reason"`
	Student              personDTO      `json:"student"`
	Absence              map[string]any `json:"absence"`
	EvidenceCount        int            `json:"evidence_count"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

func toCase(c domain.Case) caseDTO {
	var awaiting *string
	if st, ok := c.Status.AwaitedStage(); ok {
		s := string(st)
		awaiting = &s
	}
	return caseDTO{
		ID: c.ID, AttendanceRecordID: c.AttendanceRecordID, TeachingAssignmentID: c.AssignmentID, Status: string(c.Status),
		AwaitingStage: awaiting, Reason: c.Reason,
		Student: personDTO{c.StudentID, c.StudentNumber, c.StudentFirst, c.StudentLast},
		Absence: map[string]any{
			"session_date": c.SessionDate.Format(dateLayout), "attendance_status": c.RecordStatus, "group_code": c.GroupCode,
			"subject": map[string]any{"code": c.SubjectCode, "name": c.SubjectName}, "academic_term": c.TermName,
		},
		EvidenceCount: c.EvidenceCount, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type evidenceDTO struct {
	ID               uuid.UUID `json:"id"`
	OriginalFilename string    `json:"original_filename"`
	MimeType         string    `json:"mime_type"`
	SizeBytes        int64     `json:"size_bytes"`
	CreatedAt        time.Time `json:"created_at"`
}

type reviewDTO struct {
	ID         uuid.UUID      `json:"id"`
	Stage      string         `json:"stage"`
	Decision   string         `json:"decision"`
	Comment    string         `json:"comment"`
	Reviewer   map[string]any `json:"reviewer"`
	ReviewedAt time.Time      `json:"reviewed_at"`
}

type detailDTO struct {
	caseDTO
	Evidence []evidenceDTO `json:"evidence"`
	Reviews  []reviewDTO   `json:"reviews"`
}

func toDetail(d *domain.Detail) detailDTO {
	out := detailDTO{caseDTO: toCase(d.Case), Evidence: make([]evidenceDTO, len(d.Evidence)), Reviews: make([]reviewDTO, len(d.Reviews))}
	for i, e := range d.Evidence {
		out.Evidence[i] = evidenceDTO{e.ID, e.OriginalFilename, e.MimeType, e.SizeBytes, e.CreatedAt}
	}
	for i, r := range d.Reviews {
		out.Reviews[i] = reviewDTO{r.ID, string(r.Stage), string(r.Decision), r.Comment,
			map[string]any{"id": r.ReviewerID, "name": r.ReviewerName}, r.ReviewedAt}
	}
	return out
}

// --- handlers -------------------------------------------------------------------------------------------

func (h *Handler) request(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		mt = ""
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)

	var in application.RequestInput
	switch mt {
	case "application/json", "": // like the rest of the API, a missing Content-Type means JSON
		var req struct {
			AttendanceRecordID uuid.UUID `json:"attendance_record_id"`
			Reason             string    `json:"reason"`
		}
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.Error(w, r, err)
			return
		}
		in = application.RequestInput{AttendanceRecordID: req.AttendanceRecordID, Reason: req.Reason}
	case "multipart/form-data":
		in, err = parseMultipart(r)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
	default:
		httpx.Error(w, r, apperr.New(apperr.KindUnsupportedMedia, "unsupported_content_type", "use application/json or multipart/form-data"))
		return
	}
	d, err := h.svc.RequestJustification(r.Context(), p, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDetail(d))
}

func badBody(msg string) error { return apperr.New(apperr.KindValidation, "invalid_body", msg) }

// passThrough keeps *http.MaxBytesError (-> 413) recognisable; anything else is a malformed body (400).
func passThrough(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return err
	}
	return badBody("malformed multipart body")
}

// parseMultipart reads the text fields, which must come BEFORE the files, and hands the
// files over as a stream: the first file part is already positioned, the rest are pulled on demand.
func parseMultipart(r *http.Request) (application.RequestInput, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return application.RequestInput{}, badBody("malformed multipart body")
	}
	var recordID, reason string
	var haveRecord bool
	parts := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			if parts == 0 {
				return application.RequestInput{}, badBody("empty multipart body")
			}
			break
		}
		if err != nil {
			return application.RequestInput{}, passThrough(err)
		}
		parts++
		if part.FileName() != "" || part.FormName() == "evidence" {
			if part.FormName() != "evidence" {
				return application.RequestInput{}, badBody("files must use the field name \"evidence\"")
			}
			id, err := uuid.Parse(recordID)
			if err != nil || !haveRecord {
				return application.RequestInput{}, apperr.New(apperr.KindValidation, "invalid_attendance_record_id", "attendance_record_id must be a UUID sent before the files")
			}
			if strings.TrimSpace(reason) == "" {
				return application.RequestInput{}, apperr.New(apperr.KindValidation, "reason_required", "the reason is required and must be sent before the files")
			}
			return application.RequestInput{AttendanceRecordID: id, Reason: reason, Files: &multipartSource{mr: mr, first: part}}, nil
		}
		val, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
		if err != nil {
			return application.RequestInput{}, passThrough(err)
		}
		if len(val) > maxFieldBytes {
			return application.RequestInput{}, domain.ErrReasonTooLong
		}
		switch part.FormName() {
		case "attendance_record_id":
			recordID, haveRecord = strings.TrimSpace(string(val)), true
		case "reason":
			reason = string(val)
		default:
			return application.RequestInput{}, badBody("unknown form field")
		}
	}
	// no files: a request with text fields only
	id, err := uuid.Parse(recordID)
	if err != nil || !haveRecord {
		return application.RequestInput{}, apperr.New(apperr.KindValidation, "invalid_attendance_record_id", "attendance_record_id must be a UUID")
	}
	return application.RequestInput{AttendanceRecordID: id, Reason: reason}, nil
}

type multipartSource struct {
	mr    *multipart.Reader
	first *multipart.Part
}

func (m *multipartSource) Next() (*application.Upload, error) {
	part := m.first
	m.first = nil
	if part == nil {
		var err error
		part, err = m.mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if err != nil {
			return nil, passThrough(err)
		}
	}
	if part.FormName() != "evidence" {
		if part.FileName() == "" {
			return nil, domain.ErrFieldsAfterFiles
		}
		return nil, badBody("files must use the field name \"evidence\"")
	}
	// The declared Content-Type of the part is ignored on purpose: only the content counts.
	return &application.Upload{Filename: part.FileName(), Content: part}, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	items, err := h.svc.List(r.Context(), p, r.URL.Query().Get("status"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	out := make([]caseDTO, len(items))
	for i, c := range items {
		out[i] = toCase(c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	id, err := pathUUID(r, "id", domain.ErrNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDetail(d))
}

func (h *Handler) decision(w http.ResponseWriter, r *http.Request, p iddomain.Principal,
	decide func(r *http.Request, p iddomain.Principal, id uuid.UUID, in application.DecisionInput) (*domain.Detail, error)) {
	id, err := pathUUID(r, "id", domain.ErrNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := decide(r, p, id, application.DecisionInput{Decision: req.Decision, Comment: req.Comment})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDetail(d))
}

func (h *Handler) tutorDecision(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	h.decision(w, r, p, func(r *http.Request, p iddomain.Principal, id uuid.UUID, in application.DecisionInput) (*domain.Detail, error) {
		return h.svc.DecideAsTutor(r.Context(), p, id, in)
	})
}

func (h *Handler) teacherDecision(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	h.decision(w, r, p, func(r *http.Request, p iddomain.Principal, id uuid.UUID, in application.DecisionInput) (*domain.Detail, error) {
		return h.svc.DecideAsTeacher(r.Context(), p, id, in)
	})
}

func (h *Handler) downloadEvidence(w http.ResponseWriter, r *http.Request, p iddomain.Principal) {
	reqID, err := pathUUID(r, "id", domain.ErrNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	evID, err := pathUUID(r, "evidenceId", domain.ErrEvidenceNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ev, rc, err := h.svc.OpenEvidence(r.Context(), p, reqID, evID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rc.Close()
	hd := w.Header()
	hd.Set("Content-Type", ev.MimeType) // the type recorded after inspecting the content, never the client's
	hd.Set("Content-Length", strconv.FormatInt(ev.SizeBytes, 10))
	hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": ev.OriginalFilename}))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}
