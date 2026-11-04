// Package domain holds the justification entities and the review state machine.
// It knows nothing about HTTP, SQL or the physical storage of evidence.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }
func conflict(code, msg string) *apperr.Error   { return apperr.New(apperr.KindConflict, code, msg) }
func notFound(code, msg string) *apperr.Error   { return apperr.New(apperr.KindNotFound, code, msg) }

// Errors returned by the justifications module.
var (
	ErrForbidden = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")

	ErrNotFound         = notFound("justification_not_found", "justification not found")
	ErrEvidenceNotFound = notFound("evidence_not_found", "evidence not found")

	// Referenced from a request body: reported as 422, like in the other modules.
	ErrRecordNotFound = validation("attendance_record_not_found", "attendance record not found")

	ErrReasonRequired   = validation("reason_required", "the reason is required")
	ErrReasonTooLong    = validation("reason_too_long", "the reason is too long")
	ErrInvalidDecision  = validation("invalid_decision", "decision must be APPROVED or REJECTED")
	ErrCommentRequired  = validation("comment_required", "a comment is required when rejecting")
	ErrCommentTooLong   = validation("comment_too_long", "the comment is too long")
	ErrEvidenceRequired = validation("evidence_required", "at least one evidence file is required")
	ErrTooManyFiles     = validation("too_many_files", "too many evidence files")
	ErrEmptyEvidence    = validation("empty_evidence", "the evidence file is empty")
	ErrInvalidFilename  = validation("invalid_filename", "the evidence file name is not valid")
	ErrFieldsAfterFiles = validation("fields_after_files", "form fields must be sent before the evidence files")
	ErrInvalidStatus    = validation("invalid_status", "unknown status filter")

	ErrAlreadyRequested   = conflict("justification_exists", "this absence already has an open or approved justification request")
	ErrAbsenceNotEligible = conflict("absence_not_eligible", "only an ABSENT attendance record can be justified")
	ErrTermClosed         = conflict("term_closed", "the academic term is closed")
	ErrEnrollmentInactive = conflict("subject_enrollment_inactive", "the subject enrollment is not active")
	ErrFinalized          = conflict("justification_finalized", "the justification has already been finalized")
	ErrWrongStage         = conflict("wrong_review_stage", "the justification is not waiting for this review stage")

	ErrEvidenceTooLarge    = apperr.New(apperr.KindPayloadTooLarge, "evidence_too_large", "the evidence file exceeds the maximum size")
	ErrUnsupportedEvidence = apperr.New(apperr.KindUnsupportedMedia, "unsupported_evidence_type", "evidence must be a PDF, JPG or PNG file")
	ErrEvidenceMismatch    = apperr.New(apperr.KindUnsupportedMedia, "evidence_type_mismatch", "the file content does not match its extension")
)

// --- state machine -----------------------------------------------------------------------

type Status string

const (
	StatusPendingTutor    Status = "PENDING_TUTOR"
	StatusTutorRejected   Status = "TUTOR_REJECTED"
	StatusPendingTeacher  Status = "PENDING_TEACHER"
	StatusTeacherRejected Status = "TEACHER_REJECTED"
	StatusJustified       Status = "JUSTIFIED"
)

var validStatuses = map[Status]bool{StatusPendingTutor: true, StatusTutorRejected: true, StatusPendingTeacher: true, StatusTeacherRejected: true, StatusJustified: true}

func ParseStatus(s string) (Status, error) {
	if !validStatuses[Status(s)] {
		return "", ErrInvalidStatus
	}
	return Status(s), nil
}

// Terminal statuses are final: reopening is not part of the MVP.
func (s Status) Terminal() bool {
	return s == StatusTutorRejected || s == StatusTeacherRejected || s == StatusJustified
}

type Stage string

const (
	StageTutor   Stage = "TUTOR"
	StageTeacher Stage = "TEACHER"
)

type Decision string

const (
	DecisionApproved Decision = "APPROVED"
	DecisionRejected Decision = "REJECTED"
)

func ParseDecision(s string) (Decision, error) {
	switch Decision(s) {
	case DecisionApproved, DecisionRejected:
		return Decision(s), nil
	}
	return "", ErrInvalidDecision
}

// transitions is the whole state machine:
//
//	PENDING_TUTOR   --TUTOR   APPROVED--> PENDING_TEACHER
//	PENDING_TUTOR   --TUTOR   REJECTED--> TUTOR_REJECTED      (terminal)
//	PENDING_TEACHER --TEACHER APPROVED--> JUSTIFIED           (terminal)
//	PENDING_TEACHER --TEACHER REJECTED--> TEACHER_REJECTED    (terminal)
var transitions = map[Status]struct {
	stage    Stage
	approved Status
	rejected Status
}{
	StatusPendingTutor:   {StageTutor, StatusPendingTeacher, StatusTutorRejected},
	StatusPendingTeacher: {StageTeacher, StatusJustified, StatusTeacherRejected},
}

// Next returns the status reached when `stage` decides, or why it cannot:
// ErrFinalized for a finished request, ErrWrongStage when the other stage is up
// (the teacher before the tutor, or anyone deciding a stage twice).
func (s Status) Next(stage Stage, d Decision) (Status, error) {
	if s.Terminal() {
		return "", ErrFinalized
	}
	t, ok := transitions[s]
	if !ok || t.stage != stage {
		return "", ErrWrongStage
	}
	if d == DecisionApproved {
		return t.approved, nil
	}
	if d == DecisionRejected {
		return t.rejected, nil
	}
	return "", ErrInvalidDecision
}

// AwaitedStage says who must decide next; false for terminal statuses.
func (s Status) AwaitedStage() (Stage, bool) {
	t, ok := transitions[s]
	return t.stage, ok
}

// --- entities ------------------------------------------------------------------------------

const (
	MaxReasonLen     = 2000
	MaxCommentLen    = 1000
	MaxEvidenceFiles = 5 // sanity cap per request
)

type Justification struct {
	ID                 uuid.UUID
	AttendanceRecordID uuid.UUID
	StudentID          uuid.UUID
	Reason             string
	Status             Status
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewReason trims and validates the free-text reason (the model has a single text field).
func NewReason(raw string) (string, error) {
	r := strings.TrimSpace(raw)
	if r == "" {
		return "", ErrReasonRequired
	}
	if utf8.RuneCountInString(r) > MaxReasonLen {
		return "", ErrReasonTooLong
	}
	return r, nil
}

// New builds a request in its initial state, PENDING_TUTOR.
func New(id, recordID, studentID uuid.UUID, reason string) (*Justification, error) {
	r, err := NewReason(reason)
	if err != nil {
		return nil, err
	}
	return &Justification{ID: id, AttendanceRecordID: recordID, StudentID: studentID, Reason: r, Status: StatusPendingTutor}, nil
}

// Review is one entry of the append-only decision history.
type Review struct {
	ID         uuid.UUID
	RequestID  uuid.UUID
	ReviewerID uuid.UUID
	Stage      Stage
	Decision   Decision
	Comment    string
	ReviewedAt time.Time

	ReviewerName string // filled when reading
}

// NewReview validates a decision. Rejections must explain themselves; approvals may add a note.
func NewReview(id, requestID, reviewerID uuid.UUID, stage Stage, d Decision, comment string) (*Review, error) {
	c := strings.TrimSpace(comment)
	if utf8.RuneCountInString(c) > MaxCommentLen {
		return nil, ErrCommentTooLong
	}
	if d == DecisionRejected && c == "" {
		return nil, ErrCommentRequired
	}
	return &Review{ID: id, RequestID: requestID, ReviewerID: reviewerID, Stage: stage, Decision: d, Comment: c}, nil
}

// Evidence is the metadata of one private file. StorageKey is internal: it is never serialized.
type Evidence struct {
	ID               uuid.UUID
	RequestID        uuid.UUID
	StorageKey       string
	OriginalFilename string
	MimeType         string
	SizeBytes        int64
	CreatedAt        time.Time
}

// --- read models ---------------------------------------------------------------------------

// Context is the academic information around a request, read at decision/consultation
// time so authorization always reflects the CURRENT relationships.
type Context struct {
	StudentNumber string
	StudentFirst  string
	StudentLast   string
	RecordStatus  string // current attendance status
	SessionDate   time.Time
	AssignmentID  uuid.UUID
	TeacherID     uuid.UUID
	GroupTutorID  *uuid.UUID
	GroupCode     string
	SubjectCode   string
	SubjectName   string
	TermName      string
	TermStatus    string
}

// Case is a request with its context.
type Case struct {
	Justification
	Context
	EvidenceCount int
}

// Detail adds the evidence metadata and the decision history.
type Detail struct {
	Case
	Evidence []Evidence
	Reviews  []Review
}
