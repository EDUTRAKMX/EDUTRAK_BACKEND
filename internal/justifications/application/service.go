// Package application holds the justification use cases. Every method receives the
// authenticated principal and decides on role + relationship to the resource; the
// relationships are read at the moment of the operation, so a change of tutor,
// teacher or enrollment takes effect immediately.
package application

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/google/uuid"
)

type Deps struct {
	Repo       ports.Repository
	Attendance ports.Attendance
	Storage    ports.EvidenceStorage
	Audit      ports.AuditRecorder
	Tx         ports.Transactor
	NewID      func() uuid.UUID
	Log        *slog.Logger

	MaxEvidenceBytes int64
	EvidenceRequired bool
}

type Service struct{ Deps }

func NewService(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.MaxEvidenceBytes <= 0 {
		d.MaxEvidenceBytes = 10 << 20
	}
	return &Service{d}
}

func (s *Service) audit(ctx context.Context, p iddomain.Principal, action, entityType string, entityID uuid.UUID, meta map[string]any) error {
	actor := p.UserID
	return s.Audit.Record(ctx, auditdomain.Event{
		ActorUserID: &actor, Action: action, EntityType: entityType, EntityID: entityID.String(), Metadata: meta,
	})
}

// --- authorization helpers ------------------------------------------------------------------

// canView: the student who asked, the teacher of the assignment, the CURRENT tutor of the group, or a DIRECTOR.
func canView(p iddomain.Principal, c *domain.Case) bool {
	return p.HasRole(iddomain.RoleDirector) ||
		(p.HasRole(iddomain.RoleStudent) && c.StudentID == p.UserID) ||
		(p.HasRole(iddomain.RoleTeacher) && c.TeacherID == p.UserID) ||
		(p.HasRole(iddomain.RoleTutor) && c.GroupTutorID != nil && *c.GroupTutorID == p.UserID)
}

func hasAcademicRole(p iddomain.Principal) bool {
	return p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor, iddomain.RoleStudent)
}

// --- A. request ---------------------------------------------------------------------------------

// Upload is one file of a request. Content is read once, with a size cap, straight into storage.
type Upload struct {
	Filename string
	Content  io.Reader
}

// FileSource yields the files of a request one by one (io.EOF at the end). It lets the HTTP
// adapter stream a multipart body without buffering it.
type FileSource interface{ Next() (*Upload, error) }

type RequestInput struct {
	AttendanceRecordID uuid.UUID
	Reason             string
	Files              FileSource // may be nil
}

type stagedFile struct {
	Key, Name, Mime string
	Size            int64
}

// RequestJustification lets a student ask to justify one of their own absences.
//
// Database and filesystem cannot share a transaction, so the order is: (1) validate
// everything that can be validated without side effects, (2) write the files to private
// storage under random keys nothing references yet, (3) in ONE database transaction
// re-validate under locks and insert the request, its evidence metadata and the audit row,
// (4) if anything fails, delete the staged files. A crash between (2) and (3) leaves
// unreferenced files (never a database row pointing to a missing file); they are harmless
// and can be swept by comparing storage keys with justification_evidence.
func (s *Service) RequestJustification(ctx context.Context, p iddomain.Principal, in RequestInput) (*domain.Detail, error) {
	if !p.HasRole(iddomain.RoleStudent) {
		return nil, domain.ErrForbidden
	}
	j, err := domain.New(s.NewID(), in.AttendanceRecordID, p.UserID, in.Reason)
	if err != nil {
		return nil, err
	}
	if s.EvidenceRequired && in.Files == nil {
		return nil, domain.ErrEvidenceRequired
	}

	// (1) cheap checks, before accepting a single byte of any file
	abs, err := s.Attendance.Absence(ctx, in.AttendanceRecordID, false)
	if err != nil {
		return nil, err
	}
	if err := checkRequestable(p, abs); err != nil {
		return nil, err
	}
	if active, err := s.Repo.HasActive(ctx, in.AttendanceRecordID); err != nil {
		return nil, err
	} else if active {
		return nil, domain.ErrAlreadyRequested
	}

	// (2) stage the files
	staged, err := s.stage(ctx, in.Files)
	if err == nil && s.EvidenceRequired && len(staged) == 0 {
		err = domain.ErrEvidenceRequired
	}
	if err != nil {
		s.discard(ctx, staged)
		return nil, err
	}

	// (3) one transaction: lock the absence first, then insert
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		abs, err := s.Attendance.Absence(ctx, in.AttendanceRecordID, true)
		if err != nil {
			return err
		}
		if err := checkRequestable(p, abs); err != nil {
			return err
		}
		if err := s.Repo.Create(ctx, j); err != nil {
			return err
		}
		for _, f := range staged {
			e := &domain.Evidence{ID: s.NewID(), RequestID: j.ID, StorageKey: f.Key, OriginalFilename: f.Name, MimeType: f.Mime, SizeBytes: f.Size}
			if err := s.Repo.AddEvidence(ctx, e); err != nil {
				return err
			}
		}
		return s.audit(ctx, p, "justifications.requested", "justification_request", j.ID, map[string]any{
			"attendance_record_id": in.AttendanceRecordID.String(), "evidence_count": len(staged),
		})
	})
	if err != nil {
		s.discard(ctx, staged) // (4) compensation
		return nil, err
	}
	return s.Repo.Detail(ctx, j.ID)
}

// checkRequestable: the absence is the caller's own, still ABSENT, of an active enrollment in an open term.
func checkRequestable(p iddomain.Principal, a *ports.Absence) error {
	if a.StudentID != p.UserID {
		return domain.ErrForbidden
	}
	if a.Status != "ABSENT" { // PRESENT, LATE and already JUSTIFIED records cannot be justified
		return domain.ErrAbsenceNotEligible
	}
	if a.EnrollmentStatus != "ACTIVE" {
		return domain.ErrEnrollmentInactive
	}
	if a.TermStatus == "closed" {
		return domain.ErrTermClosed
	}
	return nil
}

func (s *Service) stage(ctx context.Context, files FileSource) ([]stagedFile, error) {
	var staged []stagedFile
	if files == nil {
		return nil, nil
	}
	for {
		up, err := files.Next()
		if errors.Is(err, io.EOF) {
			return staged, nil
		}
		if err != nil {
			return staged, err
		}
		if len(staged) >= domain.MaxEvidenceFiles {
			return staged, domain.ErrTooManyFiles
		}
		f, err := s.stageOne(ctx, up)
		if err != nil {
			return staged, err
		}
		staged = append(staged, *f)
	}
}

// stageOne validates by CONTENT (magic numbers vs extension), then streams the file to
// storage with a hard size cap: at most max+1 bytes are ever read from the client.
func (s *Service) stageOne(ctx context.Context, up *Upload) (*stagedFile, error) {
	head := make([]byte, domain.SniffBytes)
	n, err := io.ReadFull(up.Content, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	head = head[:n]
	typ, name, err := domain.InspectEvidence(up.Filename, head)
	if err != nil {
		return nil, err
	}
	key := storageKey(s.NewID(), typ.Ext)
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(up.Content, s.MaxEvidenceBytes+1-int64(n)))
	size, err := s.Storage.Put(ctx, key, body)
	if err != nil {
		s.discardKeys(ctx, key)
		return nil, err
	}
	if size > s.MaxEvidenceBytes {
		s.discardKeys(ctx, key)
		return nil, domain.ErrEvidenceTooLarge
	}
	return &stagedFile{Key: key, Name: name, Mime: typ.Mime, Size: size}, nil
}

// storageKey is "<2 hex>/<32 hex>.<ext>": random, never derived from the client's file name.
func storageKey(id uuid.UUID, ext string) string {
	h := strings.ReplaceAll(id.String(), "-", "")
	return h[:2] + "/" + h + "." + ext
}

func (s *Service) discard(ctx context.Context, files []stagedFile) {
	keys := make([]string, len(files))
	for i, f := range files {
		keys[i] = f.Key
	}
	s.discardKeys(ctx, keys...)
}

// discardKeys is best effort: a failure is logged (key only, never content) and the
// orphan file stays unreferenced.
func (s *Service) discardKeys(ctx context.Context, keys ...string) {
	for _, k := range keys {
		if err := s.Storage.Delete(context.WithoutCancel(ctx), k); err != nil {
			s.Log.Error("could not remove staged evidence", "storage_key", k, "error", err)
		}
	}
}
