package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	attendancenet "github.com/edutrak/backend/internal/attendance/adapters/network"
	attendancepg "github.com/edutrak/backend/internal/attendance/adapters/postgres"
	attendancesec "github.com/edutrak/backend/internal/attendance/adapters/security"
	attendanceapp "github.com/edutrak/backend/internal/attendance/application"
	auditpg "github.com/edutrak/backend/internal/audit/adapters/postgres"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	justattendance "github.com/edutrak/backend/internal/justifications/adapters/attendance"
	justpg "github.com/edutrak/backend/internal/justifications/adapters/postgres"
	juststorage "github.com/edutrak/backend/internal/justifications/adapters/storage"
	justapp "github.com/edutrak/backend/internal/justifications/application"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/google/uuid"
)

const jx = "/api/justifications"

var (
	pdfFile = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("p"), 200)...)
	jpgFile = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("j"), 200)...)
	pngFile = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte("n"), 200)...)
)

// jfx is the attendance fixture plus a private evidence directory and, for each of s1 and
// s2, one ABSENT record (created the real way: a session that is opened and closed).
type jfx struct {
	*gradeFx
	storage    string
	rec1, rec2 string
}

func newJFx(t *testing.T, extra ...func(*config.Config)) *jfx {
	t.Helper()
	dir := t.TempDir()
	fx := newGradeFxWith(t, func(c *config.Config) {
		institutional(c)
		c.StorageDir = dir
		c.MaxEvidenceMB = 1 // keeps the "too large" cases tiny
		for _, f := range extra {
			f(c)
		}
	})
	j := &jfx{gradeFx: fx, storage: dir}
	j.rec1, j.rec2 = j.newAbsences(t)
	return j
}

// newAbsences opens and closes a session: s1 and s2 both end up ABSENT.
func (j *jfx) newAbsences(t *testing.T) (rec1, rec2 string) {
	t.Helper()
	s := j.startSession(t)
	j.w.must("teacher", "POST", att+"/sessions/"+s+"/close", "", 200)
	q := `SELECT id::text FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`
	return j.w.sqlString(q, s, j.se1), j.w.sqlString(q, s, j.se2)
}

func (j *jfx) filesOnDisk(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(j.storage, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

type part struct {
	field, filename string
	data            []byte
}

func multipartBody(parts ...part) (string, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.filename == "" && p.data != nil && p.field != "evidence" {
			_ = mw.WriteField(p.field, string(p.data))
			continue
		}
		fw, _ := mw.CreateFormFile(p.field, p.filename)
		_, _ = fw.Write(p.data)
	}
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
}

func (j *jfx) submit(who, record, reason string, files ...part) resp {
	parts := []part{{field: "attendance_record_id", data: []byte(record)}, {field: "reason", data: []byte(reason)}}
	parts = append(parts, files...)
	body, ct := multipartBody(parts...)
	return j.w.do2(who, "POST", jx, body, "Content-Type", ct)
}

func (w *world) do2(who, method, path, body string, hdr ...string) resp {
	w.t.Helper()
	return w.c.do(method, path, w.token[who], body, hdr...)
}

func (j *jfx) requestJSON(who, record, reason string) resp {
	return j.w.do(who, "POST", jx, fmt.Sprintf(`{"attendance_record_id":%q,"reason":%q}`, record, reason))
}

func (j *jfx) decide(who, id, stage, decision, comment string) resp {
	return j.w.do(who, "POST", jx+"/"+id+"/"+stage+"-decision", fmt.Sprintf(`{"decision":%q,"comment":%q}`, decision, comment))
}

func (j *jfx) status(id string) string {
	return j.w.sqlString(`SELECT status FROM justification_requests WHERE id = $1`, id)
}

func (j *jfx) attendanceOf(record string) string {
	return j.w.sqlString(`SELECT status FROM attendance_records WHERE id = $1`, record)
}

// created makes a PENDING_TUTOR request for s1's absence and returns its id.
func (j *jfx) created(t *testing.T) string {
	t.Helper()
	r := j.requestJSON("s1", j.rec1, "Consulta médica")
	if r.status != 201 {
		t.Fatalf("request: %d %s", r.status, r.body)
	}
	return idOf(r.json(t))
}

// pendingTeacher brings it to PENDING_TEACHER.
func (j *jfx) pendingTeacher(t *testing.T) string {
	t.Helper()
	id := j.created(t)
	if r := j.decide("tutor", id, "tutor", "APPROVED", ""); r.status != 200 {
		t.Fatalf("tutor approval: %d %s", r.status, r.body)
	}
	return id
}

func TestJustificationsAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	for _, ep := range [][2]string{
		{"POST", jx}, {"GET", jx}, {"GET", jx + "/" + id}, {"POST", jx + "/" + id + "/tutor-decision"},
		{"POST", jx + "/" + id + "/teacher-decision"}, {"GET", jx + "/" + id + "/evidence/" + id},
	} {
		if r := w.c.do(ep[0], ep[1], "", `{}`); r.status != 401 {
			t.Errorf("%s %s without token: %d", ep[0], ep[1], r.status)
		}
		if r := w.c.do(ep[0], ep[1], "garbage", `{}`); r.status != 401 {
			t.Errorf("%s %s with a garbage token: %d", ep[0], ep[1], r.status)
		}
	}
}

func TestJustificationRequestRules(t *testing.T) {
	j := newJFx(t)
	w := j.w

	// the record must be an ABSENT of the caller
	s := j.startSession(t)
	w.must("teacher", "POST", att+"/sessions/"+s+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, j.se1), 201)
	w.must("teacher", "POST", att+"/sessions/"+s+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"LATE"}`, j.se2), 201)
	w.must("teacher", "POST", att+"/sessions/"+s+"/close", "", 200)
	present := w.sqlString(`SELECT id::text FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`, s, j.se1)
	late := w.sqlString(`SELECT id::text FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`, s, j.se2)

	for name, c := range map[string]struct {
		who, rec, code string
		status         int
	}{
		"PRESENT record":        {"s1", present, "absence_not_eligible", 409},
		"LATE record":           {"s2", late, "absence_not_eligible", 409},
		"another student's":     {"s1", j.rec2, "forbidden", 403},
		"unknown record":        {"s1", uuid.NewString(), "attendance_record_not_found", 422},
		"teacher":               {"teacher", j.rec1, "forbidden", 403},
		"tutor":                 {"tutor", j.rec1, "forbidden", 403},
		"director":              {"director", j.rec1, "forbidden", 403},
		"admin":                 {"admin", j.rec1, "forbidden", 403},
		"student outside group": {"s3", j.rec1, "forbidden", 403},
	} {
		if r := j.requestJSON(c.who, c.rec, "Motivo"); r.status != c.status || r.errCode(t) != c.code {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	for name, body := range map[string]string{
		"student_id in the body": fmt.Sprintf(`{"attendance_record_id":%q,"reason":"x","student_id":%q}`, j.rec1, w.ids["s2"]),
		"status in the body":     fmt.Sprintf(`{"attendance_record_id":%q,"reason":"x","status":"JUSTIFIED"}`, j.rec1),
		"not json":               `{`,
	} {
		if r := w.do("s1", "POST", jx, body); r.status != 400 || r.errCode(t) != "invalid_body" {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := j.requestJSON("s1", j.rec1, "   "); r.status != 422 || r.errCode(t) != "reason_required" {
		t.Errorf("blank reason: %d %s", r.status, r.body)
	}
	if r := j.requestJSON("s1", j.rec1, strings.Repeat("a", 2001)); r.status != 422 || r.errCode(t) != "reason_too_long" {
		t.Errorf("long reason: %d %s", r.status, r.body)
	}
	if r := w.do2("s1", "POST", jx, "reason=x", "Content-Type", "text/plain"); r.status != 415 || r.errCode(t) != "unsupported_content_type" {
		t.Errorf("text/plain: %d %s", r.status, r.body)
	}
	if n := w.sqlInt(`SELECT count(*) FROM justification_requests`); n != 0 {
		t.Fatalf("rejected requests stored %d rows", n)
	}

	// a valid request, JSON (no evidence)
	r := j.requestJSON("s1", j.rec1, "Consulta médica")
	if r.status != 201 {
		t.Fatalf("request: %d %s", r.status, r.body)
	}
	m := r.json(t)
	if m["status"] != "PENDING_TUTOR" || m["awaiting_stage"] != "TUTOR" || m["reason"] != "Consulta médica" || m["evidence_count"] != float64(0) {
		t.Fatalf("request = %v", m)
	}
	if m["student"].(map[string]any)["id"] != w.ids["s1"] || m["absence"].(map[string]any)["attendance_status"] != "ABSENT" {
		t.Fatalf("request = %v", m)
	}
	// ...and the duplicate
	if r := j.requestJSON("s1", j.rec1, "otra vez"); r.status != 409 || r.errCode(t) != "justification_exists" {
		t.Errorf("duplicate: %d %s", r.status, r.body)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'justifications.requested' AND actor_user_id = $1`, w.ids["s1"]); n != 1 {
		t.Errorf("requested audit rows = %d", n)
	}
}

func TestJustificationEvidenceUpload(t *testing.T) {
	j := newJFx(t)
	w := j.w

	t.Run("valid PDF, JPG and PNG are stored privately under generated keys", func(t *testing.T) {
		r := j.submit("s1", j.rec1, "Consulta médica",
			part{"evidence", "constancia.pdf", pdfFile}, part{"evidence", "receta.JPG", jpgFile}, part{"evidence", "foto.png", pngFile})
		if r.status != 201 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		m := r.json(t)
		ev := m["evidence"].([]any)
		if len(ev) != 3 || m["evidence_count"] != float64(3) {
			t.Fatalf("evidence = %v", ev)
		}
		got := map[string]string{}
		for _, e := range ev {
			em := e.(map[string]any)
			got[em["original_filename"].(string)] = em["mime_type"].(string)
			for k := range em {
				if strings.Contains(k, "key") || strings.Contains(k, "path") || strings.Contains(k, "url") {
					t.Errorf("evidence exposes %q", k)
				}
			}
		}
		if got["constancia.pdf"] != "application/pdf" || got["receta.JPG"] != "image/jpeg" || got["foto.png"] != "image/png" {
			t.Errorf("mime types = %v", got)
		}
		for _, banned := range []string{w.cfg.StorageDir, "storage_key", ".pdf\"", "ab/"} {
			if banned != ".pdf\"" && banned != "ab/" && strings.Contains(r.body, banned) {
				t.Errorf("response leaks %q", banned)
			}
		}
		// on disk: three files, random names, nothing derived from the client's names, inside the private dir
		files := j.filesOnDisk(t)
		if len(files) != 3 {
			t.Fatalf("files on disk = %v", files)
		}
		keyRe := regexp.MustCompile(`^[0-9a-f]{2}[/\\][0-9a-f]{32}\.(pdf|jpg|png)$`)
		for _, f := range files {
			rel, _ := filepath.Rel(j.storage, f)
			if !keyRe.MatchString(rel) {
				t.Errorf("stored as %q", rel)
			}
		}
		rows := w.sqlInt(`SELECT count(*) FROM justification_evidence e JOIN justification_requests r ON r.id = e.justification_request_id WHERE r.id = $1`, idOf(m))
		if rows != 3 {
			t.Errorf("evidence rows = %d", rows)
		}
		if n := w.sqlInt(`SELECT count(*) FROM justification_evidence WHERE storage_key LIKE '%constancia%' OR storage_key LIKE '%receta%' OR storage_key LIKE '%..%'`); n != 0 {
			t.Error("a storage key was derived from a client file name")
		}
		if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%storage%' OR metadata::text LIKE '%constancia%'`); n != 0 {
			t.Error("audit metadata must not mention files or storage")
		}
	})

	t.Run("rejected files leave nothing behind", func(t *testing.T) {
		j := newJFx(t)
		before := len(j.filesOnDisk(t))
		exe := append([]byte("MZ\x90\x00\x03"), bytes.Repeat([]byte("x"), 100)...)
		big := append([]byte("%PDF-"), bytes.Repeat([]byte("a"), 1<<20)...) // 1 MiB + 5 > the 1 MiB limit
		var six []part
		for i := 0; i < 6; i++ {
			six = append(six, part{"evidence", fmt.Sprintf("f%d.pdf", i), pdfFile})
		}
		for name, c := range map[string]struct {
			files []part
			code  string
			http  int
		}{
			"executable disguised as pdf": {[]part{{"evidence", "factura.pdf", exe}}, "unsupported_evidence_type", 415},
			"html disguised as png":       {[]part{{"evidence", "x.png", []byte("<html><script>1</script></html>")}}, "unsupported_evidence_type", 415},
			"png content, pdf extension":  {[]part{{"evidence", "x.pdf", pngFile}}, "evidence_type_mismatch", 415},
			"pdf content, exe extension":  {[]part{{"evidence", "x.exe", pdfFile}}, "unsupported_evidence_type", 415},
			"no extension":                {[]part{{"evidence", "constancia", pdfFile}}, "unsupported_evidence_type", 415},
			"empty file":                  {[]part{{"evidence", "x.pdf", []byte{}}}, "empty_evidence", 422},
			"over the size limit":         {[]part{{"evidence", "big.pdf", big}}, "evidence_too_large", 413},
			"a good file then a bad one":  {[]part{{"evidence", "ok.pdf", pdfFile}, {"evidence", "bad.pdf", exe}}, "unsupported_evidence_type", 415},
			"too many files":              {six, "too_many_files", 422},
			"wrong field name":            {[]part{{"attachment", "a.pdf", pdfFile}}, "invalid_body", 400},
		} {
			// "empty file" needs a non-nil body to be sent as a file part
			for i := range c.files {
				if c.files[i].data != nil && len(c.files[i].data) == 0 {
					c.files[i].data = []byte{}
				}
			}
			r := j.submit("s1", j.rec1, "Motivo", c.files...)
			if r.status != c.http || r.errCode(t) != c.code {
				t.Errorf("%s: %d %s", name, r.status, r.body)
			}
		}
		if n := w.sqlInt(`SELECT count(*) FROM justification_requests`); false && n != 0 {
			t.Fatal("unreachable")
		}
		if n := j.w.sqlInt(`SELECT count(*) FROM justification_requests`); n != 0 {
			t.Fatalf("rejected uploads created %d requests", n)
		}
		if n := j.w.sqlInt(`SELECT count(*) FROM justification_evidence`); n != 0 {
			t.Fatalf("rejected uploads left %d evidence rows", n)
		}
		if after := len(j.filesOnDisk(t)); after != before {
			t.Fatalf("files on disk: %d -> %d: staged files must be removed again", before, after)
		}
	})

	t.Run("form fields must precede the files", func(t *testing.T) {
		j := newJFx(t)
		body, ct := multipartBody(part{"attendance_record_id", "", []byte(j.rec1)}, part{"evidence", "a.pdf", pdfFile}, part{"reason", "", []byte("tarde")})
		if r := j.w.do2("s1", "POST", jx, body, "Content-Type", ct); r.status != 422 || r.errCode(t) != "reason_required" {
			t.Errorf("reason after the files: %d %s", r.status, r.body)
		}
		body, ct = multipartBody(part{"attendance_record_id", "", []byte(j.rec1)}, part{"reason", "", []byte("ok")}, part{"evidence", "a.pdf", pdfFile}, part{"reason", "", []byte("otra")})
		if r := j.w.do2("s1", "POST", jx, body, "Content-Type", ct); r.status != 422 || r.errCode(t) != "fields_after_files" {
			t.Errorf("a text field after the files: %d %s", r.status, r.body)
		}
		if j.w.sqlInt(`SELECT count(*) FROM justification_requests`) != 0 || len(j.filesOnDisk(t)) != 0 {
			t.Error("rejected multipart bodies left traces")
		}
		body, ct = multipartBody(part{"evidence", "a.pdf", pdfFile}, part{"reason", "", []byte("x")})
		if r := j.w.do2("s1", "POST", jx, body, "Content-Type", ct); r.status != 422 {
			t.Errorf("file before the record id: %d %s", r.status, r.body)
		}
		body, ct = multipartBody(part{"attendance_record_id", "", []byte("not-a-uuid")}, part{"reason", "", []byte("x")})
		if r := j.w.do2("s1", "POST", jx, body, "Content-Type", ct); r.status != 422 || r.errCode(t) != "invalid_attendance_record_id" {
			t.Errorf("bad id: %d %s", r.status, r.body)
		}
		body, ct = multipartBody(part{"attendance_record_id", "", []byte(j.rec1)}, part{"student_id", "", []byte(j.w.ids["s2"])})
		if r := j.w.do2("s1", "POST", jx, body, "Content-Type", ct); r.status != 400 {
			t.Errorf("unknown field: %d %s", r.status, r.body)
		}
		if r := j.w.do2("s1", "POST", jx, "--broken", "Content-Type", "multipart/form-data; boundary=zzz"); r.status != 400 {
			t.Errorf("broken multipart: %d", r.status)
		}
	})

	t.Run("hostile file names never become paths", func(t *testing.T) {
		j := newJFx(t)
		r := j.submit("s1", j.rec1, "Motivo", part{"evidence", "../../../outside/evil.pdf", pdfFile})
		if r.status != 201 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		name := r.json(t)["evidence"].([]any)[0].(map[string]any)["original_filename"].(string)
		if strings.ContainsAny(name, `/\`) {
			t.Errorf("display name %q still has a path", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(j.storage), "outside")); err == nil {
			t.Fatal("something was written outside the storage directory")
		}
		if files := j.filesOnDisk(t); len(files) != 1 || !strings.HasPrefix(files[0], j.storage) {
			t.Fatalf("files = %v", files)
		}
	})
}

func TestJustificationEvidenceCanBeRequired(t *testing.T) {
	j := newJFx(t, func(c *config.Config) { c.JustificationEvidenceRequired = true })
	if r := j.requestJSON("s1", j.rec1, "Sin archivo"); r.status != 422 || r.errCode(t) != "evidence_required" {
		t.Fatalf("JSON without evidence: %d %s", r.status, r.body)
	}
	if r := j.submit("s1", j.rec1, "Sin archivo"); r.status != 422 || r.errCode(t) != "evidence_required" {
		t.Fatalf("multipart without evidence: %d %s", r.status, r.body)
	}
	if n := j.w.sqlInt(`SELECT count(*) FROM justification_requests`); n != 0 {
		t.Fatal("an incomplete request was stored")
	}
	if r := j.submit("s1", j.rec1, "Con archivo", part{"evidence", "a.pdf", pdfFile}); r.status != 201 {
		t.Fatalf("with evidence: %d %s", r.status, r.body)
	}
}

func TestJustificationFullFlowJustifiesTheAbsence(t *testing.T) {
	j := newJFx(t)
	w := j.w
	id := idOf(j.submit("s1", j.rec1, "Consulta médica", part{"evidence", "c.pdf", pdfFile}).json(t))

	// the teacher cannot go first
	if r := j.decide("teacher", id, "teacher", "APPROVED", ""); r.status != 409 || r.errCode(t) != "wrong_review_stage" {
		t.Fatalf("teacher first: %d %s", r.status, r.body)
	}
	// who may decide the tutor stage
	for _, who := range []string{"tutor2", "teacher", "teacher2", "s1", "director", "admin"} {
		if r := j.decide(who, id, "tutor", "APPROVED", ""); r.status != 403 {
			t.Errorf("%s on the tutor stage: %d %s", who, r.status, r.body)
		}
	}
	for name, c := range map[string]struct{ dec, comment, code string }{
		"unknown decision":      {"MAYBE", "", "invalid_decision"},
		"missing decision":      {"", "", "invalid_decision"},
		"rejection w/o comment": {"REJECTED", "  ", "comment_required"},
	} {
		if r := j.decide("tutor", id, "tutor", c.dec, c.comment); r.status != 422 || r.errCode(t) != c.code {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	w.expect("tutor", "POST", jx+"/"+id+"/tutor-decision", `{"decision":"APPROVED","reviewer_id":"`+w.ids["tutor2"]+`"}`, 400, "invalid_body")
	w.expect("tutor", "POST", jx+"/"+uuid.NewString()+"/tutor-decision", `{"decision":"APPROVED"}`, 404, "justification_not_found")
	w.expect("tutor", "POST", jx+"/zzz/tutor-decision", `{"decision":"APPROVED"}`, 404, "justification_not_found")
	if j.status(id) != "PENDING_TUTOR" {
		t.Fatal("rejected attempts moved the request")
	}

	r := j.decide("tutor", id, "tutor", "APPROVED", "Documento legible")
	if r.status != 200 || r.json(t)["status"] != "PENDING_TEACHER" || r.json(t)["awaiting_stage"] != "TEACHER" {
		t.Fatalf("tutor approval: %d %s", r.status, r.body)
	}
	if j.attendanceOf(j.rec1) != "ABSENT" {
		t.Fatal("a tutor approval must not justify the absence")
	}
	// the tutor cannot decide twice or take the final decision
	if r := j.decide("tutor", id, "tutor", "APPROVED", ""); r.status != 409 || r.errCode(t) != "wrong_review_stage" {
		t.Errorf("second tutor decision: %d %s", r.status, r.body)
	}
	if r := j.decide("tutor", id, "teacher", "APPROVED", ""); r.status != 403 {
		t.Errorf("tutor on the teacher stage: %d", r.status)
	}
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		if r := j.decide(who, id, "teacher", "APPROVED", ""); r.status != 403 {
			t.Errorf("%s on the teacher stage: %d %s", who, r.status, r.body)
		}
	}
	if j.attendanceOf(j.rec1) != "ABSENT" {
		t.Fatal("forbidden callers justified the absence")
	}

	final := j.decide("teacher", id, "teacher", "APPROVED", "Aprobado")
	if final.status != 200 || final.json(t)["status"] != "JUSTIFIED" || final.json(t)["awaiting_stage"] != nil {
		t.Fatalf("final approval: %d %s", final.status, final.body)
	}
	if j.attendanceOf(j.rec1) != "JUSTIFIED" {
		t.Fatal("the final approval must turn the attendance record into JUSTIFIED")
	}
	reviews := final.json(t)["reviews"].([]any)
	if len(reviews) != 2 {
		t.Fatalf("reviews = %v", reviews)
	}
	r0, r1 := reviews[0].(map[string]any), reviews[1].(map[string]any)
	if r0["stage"] != "TUTOR" || r0["decision"] != "APPROVED" || r0["comment"] != "Documento legible" || r0["reviewer"].(map[string]any)["id"] != w.ids["tutor"] ||
		r1["stage"] != "TEACHER" || r1["comment"] != "Aprobado" || r1["reviewer"].(map[string]any)["id"] != w.ids["teacher"] {
		t.Fatalf("history = %v", reviews)
	}
	// finished requests are immutable
	for _, dec := range []string{"APPROVED", "REJECTED"} {
		if r := j.decide("teacher", id, "teacher", dec, "x"); r.status != 409 || r.errCode(t) != "justification_finalized" {
			t.Errorf("after the end (%s): %d %s", dec, r.status, r.body)
		}
		if r := j.decide("tutor", id, "tutor", dec, "x"); r.status != 409 || r.errCode(t) != "justification_finalized" {
			t.Errorf("tutor after the end (%s): %d %s", dec, r.status, r.body)
		}
	}
	// the request/attendance pair is in sync, and the audit trail is complete
	if n := w.sqlInt(`SELECT count(*) FROM justification_requests j JOIN attendance_records a ON a.id = j.attendance_record_id
		WHERE (j.status = 'JUSTIFIED') <> (a.status = 'JUSTIFIED')`); n != 0 {
		t.Fatalf("%d requests out of sync with their attendance record", n)
	}
	for action, entity := range map[string]string{
		"justifications.requested": id, "justifications.tutor_approved": id, "justifications.teacher_approved": id, "attendance.justified": j.rec1,
	} {
		if w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = $1 AND entity_id = $2`, action, entity) != 1 {
			t.Errorf("audit row %s missing for %s", action, entity)
		}
	}
	// the student's attendance view reflects it, and the equivalence indicator ignores justified records
	m := w.must("s1", "GET", att+"/students/"+w.ids["s1"], "", 200)
	for _, d := range m["data"].([]any) {
		sheet := d.(map[string]any)
		if sheet["subject"].(map[string]any)["code"] == "MAT1" {
			a := sheet["attendance"].(map[string]any)
			if a["justified"] != float64(1) || a["absent"] != float64(0) || a["effective_absences"] != float64(0) {
				t.Errorf("attendance after justification = %v", a)
			}
		}
	}
	// the public attendance endpoints still cannot create or touch JUSTIFIED records
	w.expect("teacher", "PATCH", att+"/records/"+j.rec1, `{"status":"PRESENT"}`, 409, "record_justified")
	w.expect("teacher", "PATCH", att+"/records/"+j.rec2, `{"status":"JUSTIFIED"}`, 422, "status_not_allowed")
	if j.attendanceOf(j.rec2) != "ABSENT" {
		t.Fatal("the public PATCH justified a record")
	}
	// no new request on a justified absence
	if r := j.requestJSON("s1", j.rec1, "otra"); r.status != 409 {
		t.Errorf("request on a JUSTIFIED absence: %d %s", r.status, r.body)
	}
}

func TestJustificationRejectionsKeepTheAbsenceAndTheHistory(t *testing.T) {
	j := newJFx(t)
	w := j.w
	id := j.created(t)
	if r := j.decide("tutor", id, "tutor", "REJECTED", ""); r.status != 422 || r.errCode(t) != "comment_required" {
		t.Fatalf("rejection without comment: %d %s", r.status, r.body)
	}
	r := j.decide("tutor", id, "tutor", "REJECTED", "No hay comprobante")
	if r.status != 200 || r.json(t)["status"] != "TUTOR_REJECTED" || r.json(t)["awaiting_stage"] != nil {
		t.Fatalf("tutor rejection: %d %s", r.status, r.body)
	}
	if r := j.decide("teacher", id, "teacher", "APPROVED", ""); r.status != 409 || r.errCode(t) != "justification_finalized" {
		t.Errorf("teacher on a tutor-rejected request: %d %s", r.status, r.body)
	}
	if j.attendanceOf(j.rec1) != "ABSENT" {
		t.Fatal("attendance must stay ABSENT")
	}
	// a NEW request is allowed after a rejection; the old one and its review stay in the history
	second := j.requestJSON("s1", j.rec1, "Adjunto más información")
	if second.status != 201 {
		t.Fatalf("new request after a rejection: %d %s", second.status, second.body)
	}
	id2 := idOf(second.json(t))
	j.decide("tutor", id2, "tutor", "APPROVED", "")
	tr := j.decide("teacher", id2, "teacher", "REJECTED", "No corresponde a la fecha")
	if tr.status != 200 || tr.json(t)["status"] != "TEACHER_REJECTED" {
		t.Fatalf("teacher rejection: %d %s", tr.status, tr.body)
	}
	if j.attendanceOf(j.rec1) != "ABSENT" {
		t.Fatal("a teacher rejection must keep the attendance ABSENT")
	}
	revs := tr.json(t)["reviews"].([]any)
	if len(revs) != 2 || revs[0].(map[string]any)["decision"] != "APPROVED" || revs[0].(map[string]any)["stage"] != "TUTOR" {
		t.Fatalf("the tutor's approval must survive the teacher's rejection: %v", revs)
	}
	if n := w.sqlInt(`SELECT count(*) FROM justification_requests WHERE attendance_record_id = $1`, j.rec1); n != 2 {
		t.Fatalf("history: %d requests, rejected ones must be kept", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM justification_reviews`); n != 3 {
		t.Fatalf("reviews = %d, want 3 (tutor reject; tutor approve + teacher reject)", n)
	}
	// and after a final rejection a third request is possible again
	if r := j.requestJSON("s1", j.rec1, "Tercer intento"); r.status != 201 {
		t.Errorf("request after a teacher rejection: %d %s", r.status, r.body)
	}
}

func TestJustificationReads(t *testing.T) {
	j := newJFx(t)
	w := j.w
	id := idOf(j.submit("s1", j.rec1, "Consulta médica", part{"evidence", "constancia médica.pdf", pdfFile}).json(t))
	id2 := idOf(j.submit("s2", j.rec2, "Gripe", part{"evidence", "gripe.png", pngFile}).json(t))
	j.decide("tutor", id2, "tutor", "APPROVED", "")

	count := func(who, query string) int {
		t.Helper()
		return len(w.must(who, "GET", jx+query, "", 200)["data"].([]any))
	}
	t.Run("lists are filtered by role and relationship", func(t *testing.T) {
		for who, want := range map[string]int{"s1": 1, "s2": 1, "s3": 0, "tutor": 2, "tutor2": 0, "teacher": 2, "teacher2": 0, "director": 2} {
			if got := count(who, ""); got != want {
				t.Errorf("%s sees %d requests, want %d", who, got, want)
			}
		}
		w.expect("admin", "GET", jx, "", 403, "forbidden")
		if count("tutor", "?status=PENDING_TUTOR") != 1 || count("tutor", "?status=PENDING_TEACHER") != 1 || count("teacher", "?status=PENDING_TEACHER") != 1 || count("tutor", "?status=JUSTIFIED") != 0 {
			t.Error("status filter")
		}
		w.expect("tutor", "GET", jx+"?status=BOGUS", "", 422, "invalid_status")
		items := w.must("tutor", "GET", jx+"?status=PENDING_TUTOR", "", 200)["data"].([]any)
		if item := items[0].(map[string]any); item["evidence_count"] != float64(1) || item["student"].(map[string]any)["institutional_number"] == nil {
			t.Errorf("list item = %v", item)
		}
	})

	t.Run("detail", func(t *testing.T) {
		for _, who := range []string{"s1", "tutor", "teacher", "director"} {
			m := w.must(who, "GET", jx+"/"+id, "", 200)
			if m["id"] != id || len(m["evidence"].([]any)) != 1 {
				t.Errorf("%s: %v", who, m)
			}
		}
		for _, who := range []string{"s2", "s3", "tutor2", "teacher2", "admin"} {
			w.expect(who, "GET", jx+"/"+id, "", 403, "forbidden")
		}
		w.expect("director", "GET", jx+"/"+uuid.NewString(), "", 404, "justification_not_found")
		w.expect("director", "GET", jx+"/zzz", "", 404, "justification_not_found")
		// the student sees the decisions and comments about their own request
		d := w.must("s2", "GET", jx+"/"+id2, "", 200)
		if revs := d["reviews"].([]any); len(revs) != 1 || revs[0].(map[string]any)["stage"] != "TUTOR" {
			t.Errorf("s2 history = %v", d["reviews"])
		}
	})

	t.Run("evidence download is authorized every time", func(t *testing.T) {
		ev := w.must("s1", "GET", jx+"/"+id, "", 200)["evidence"].([]any)[0].(map[string]any)
		path := jx + "/" + id + "/evidence/" + idOf(ev)
		for _, who := range []string{"s1", "tutor", "teacher", "director"} {
			r := w.do(who, "GET", path, "")
			if r.status != 200 || r.body != string(pdfFile) {
				t.Errorf("%s: %d (%d bytes)", who, r.status, len(r.body))
			}
		}
		// headers: type from the inspected content, never sniffable, never cached, forced download
		rec := w.c.doFrom("192.0.2.10:1", "GET", path, w.token["s1"], "")
		_ = rec
		for _, who := range []string{"s2", "s3", "tutor2", "teacher2", "admin"} {
			if r := w.do(who, "GET", path, ""); r.status != 403 || strings.Contains(r.body, "%PDF") {
				t.Errorf("%s: %d %s", who, r.status, r.body)
			}
		}
		// another request's evidence cannot be pulled through this request
		ev2 := w.must("s2", "GET", jx+"/"+id2, "", 200)["evidence"].([]any)[0].(map[string]any)
		w.expect("s1", "GET", jx+"/"+id+"/evidence/"+idOf(ev2), "", 404, "evidence_not_found")
		w.expect("s1", "GET", jx+"/"+id+"/evidence/"+uuid.NewString(), "", 404, "evidence_not_found")
		w.expect("s1", "GET", jx+"/"+id+"/evidence/zzz", "", 404, "evidence_not_found")
		w.expect("s1", "GET", jx+"/"+uuid.NewString()+"/evidence/"+idOf(ev), "", 404, "justification_not_found")

		// relationships are re-read on every call: a reassigned tutor/teacher gains and loses access at once
		ctx := context.Background()
		w.db.Q(ctx).Exec(ctx, `UPDATE groups SET tutor_id = $1 WHERE id = $2`, w.ids["tutor2"], j.group)
		if r := w.do("tutor", "GET", path, ""); r.status != 403 {
			t.Errorf("the former tutor keeps access: %d", r.status)
		}
		if r := w.do("tutor2", "GET", path, ""); r.status != 200 {
			t.Errorf("the new tutor cannot read: %d", r.status)
		}
		w.db.Q(ctx).Exec(ctx, `UPDATE teaching_assignments SET teacher_id = $1 WHERE id = $2`, w.ids["teacher2"], j.assignment)
		if r := w.do("teacher", "GET", path, ""); r.status != 403 {
			t.Errorf("the former teacher keeps access: %d", r.status)
		}
		if r := w.do("teacher2", "GET", path, ""); r.status != 200 {
			t.Errorf("the new teacher cannot read: %d", r.status)
		}
		// the tutor change also moves the decision right
		if r := j.decide("tutor", id, "tutor", "APPROVED", ""); r.status != 403 {
			t.Errorf("the former tutor can still decide: %d", r.status)
		}
		if r := j.decide("tutor2", id, "tutor", "APPROVED", ""); r.status != 200 {
			t.Errorf("the new tutor cannot decide: %d %s", r.status, r.body)
		}
	})
}

func TestJustificationEvidenceDownloadHeaders(t *testing.T) {
	j := newJFx(t)
	w := j.w
	id := idOf(j.submit("s1", j.rec1, "x", part{"evidence", "constancia médica.pdf", pdfFile}).json(t))
	ev := w.must("s1", "GET", jx+"/"+id, "", 200)["evidence"].([]any)[0].(map[string]any)
	req := w.c.do // keep the response headers: use the recorder through a tiny wrapper
	_ = req
	h := downloadHeaders(w, "s1", jx+"/"+id+"/evidence/"+idOf(ev))
	if h.Get("Content-Type") != "application/pdf" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Cache-Control"), "no-store") || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("headers = %v", h)
	}
	if strings.Contains(h.Get("Content-Disposition"), "/") {
		t.Fatalf("disposition = %s", h.Get("Content-Disposition"))
	}
}

func TestJustificationConcurrency(t *testing.T) {
	t.Run("simultaneous identical requests create exactly one", func(t *testing.T) {
		j := newJFx(t)
		var wg sync.WaitGroup
		var mu sync.Mutex
		created, conflicts := 0, 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := j.submit("s1", j.rec1, "Consulta", part{"evidence", "a.pdf", pdfFile})
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 201:
					created++
				case 409:
					conflicts++
				default:
					t.Errorf("unexpected %d: %s", r.status, r.body)
				}
			}()
		}
		wg.Wait()
		if created != 1 || conflicts != 7 {
			t.Fatalf("created=%d conflicts=%d", created, conflicts)
		}
		if n := j.w.sqlInt(`SELECT count(*) FROM justification_requests`); n != 1 {
			t.Fatalf("%d requests", n)
		}
		// the 7 losers uploaded a file each and their compensation removed it
		if files := j.filesOnDisk(t); len(files) != 1 {
			t.Fatalf("files on disk = %d, want exactly the winner's", len(files))
		}
		if n := j.w.sqlInt(`SELECT count(*) FROM justification_evidence`); n != 1 {
			t.Fatalf("evidence rows = %d", n)
		}
	})

	t.Run("simultaneous final approvals: one wins, the rest are refused", func(t *testing.T) {
		j := newJFx(t)
		id := j.pendingTeacher(t)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, conflicts := 0, 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := j.decide("teacher", id, "teacher", "APPROVED", "")
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 200:
					ok++
				case 409:
					conflicts++
				default:
					t.Errorf("unexpected %d: %s", r.status, r.body)
				}
			}()
		}
		wg.Wait()
		if ok != 1 || conflicts != 7 {
			t.Fatalf("ok=%d conflicts=%d", ok, conflicts)
		}
		if j.status(id) != "JUSTIFIED" || j.attendanceOf(j.rec1) != "JUSTIFIED" {
			t.Fatal("request and attendance must both be JUSTIFIED")
		}
		w := j.w
		if w.sqlInt(`SELECT count(*) FROM justification_reviews WHERE justification_request_id = $1 AND stage = 'TEACHER'`, id) != 1 ||
			w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.justified' AND entity_id = $1`, j.rec1) != 1 ||
			w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'justifications.teacher_approved' AND entity_id = $1`, id) != 1 {
			t.Fatal("exactly one teacher review and one audit trail expected")
		}
	})

	t.Run("a tutor approval racing the teacher's decision: always a coherent sequence", func(t *testing.T) {
		for round := 0; round < 10; round++ {
			j := newJFx(t)
			id := j.created(t)
			var wg sync.WaitGroup
			start := make(chan struct{})
			statuses := make([]int, 3)
			wg.Add(3)
			go func() { defer wg.Done(); <-start; statuses[0] = j.decide("tutor", id, "tutor", "APPROVED", "").status }()
			go func() {
				defer wg.Done()
				<-start
				statuses[1] = j.decide("teacher", id, "teacher", "APPROVED", "").status
			}()
			go func() {
				defer wg.Done()
				<-start
				statuses[2] = j.decide("teacher", id, "teacher", "REJECTED", "no").status
			}()
			close(start)
			wg.Wait()
			if statuses[0] != 200 {
				t.Fatalf("round %d: the tutor's approval must succeed, got %d", round, statuses[0])
			}
			w := j.w
			st := j.status(id)
			stages := w.sqlString(`SELECT COALESCE(string_agg(stage || ':' || decision, ',' ORDER BY reviewed_at, stage DESC), '') FROM justification_reviews WHERE justification_request_id = $1`, id)
			switch st {
			case "PENDING_TEACHER": // both teacher attempts arrived before the tutor
				if stages != "TUTOR:APPROVED" || j.attendanceOf(j.rec1) != "ABSENT" {
					t.Fatalf("round %d: %s / %s / %s", round, st, stages, j.attendanceOf(j.rec1))
				}
			case "JUSTIFIED":
				if !strings.HasPrefix(stages, "TUTOR:APPROVED,TEACHER:APPROVED") || j.attendanceOf(j.rec1) != "JUSTIFIED" {
					t.Fatalf("round %d: %s / %s / %s", round, st, stages, j.attendanceOf(j.rec1))
				}
			case "TEACHER_REJECTED":
				if !strings.HasPrefix(stages, "TUTOR:APPROVED,TEACHER:REJECTED") || j.attendanceOf(j.rec1) != "ABSENT" {
					t.Fatalf("round %d: %s / %s / %s", round, st, stages, j.attendanceOf(j.rec1))
				}
			default:
				t.Fatalf("round %d: impossible status %s (%v)", round, st, statuses)
			}
			if n := w.sqlInt(`SELECT count(*) FROM justification_reviews WHERE justification_request_id = $1 AND stage = 'TEACHER'`, id); n > 1 {
				t.Fatalf("round %d: %d teacher reviews", round, n)
			}
		}
	})

	t.Run("an attendance correction racing the final approval never leaves them out of sync", func(t *testing.T) {
		j := newJFx(t)
		w := j.w
		var sawApproved, sawCorrected bool
		for round := 0; round < 14; round++ {
			if round > 0 {
				j.rec1, j.rec2 = j.newAbsences(t)
			}
			id := j.pendingTeacher(t)
			var wg sync.WaitGroup
			start := make(chan struct{})
			var approve, correct resp
			wg.Add(2)
			go func() { defer wg.Done(); <-start; approve = j.decide("teacher", id, "teacher", "APPROVED", "") }()
			go func() {
				defer wg.Done()
				<-start
				correct = w.do("teacher", "PATCH", att+"/records/"+j.rec1, `{"status":"PRESENT"}`)
			}()
			close(start)
			wg.Wait()

			st, rec := j.status(id), j.attendanceOf(j.rec1)
			switch {
			case approve.status == 200: // approval first: the correction must have been refused
				sawApproved = true
				if st != "JUSTIFIED" || rec != "JUSTIFIED" || correct.status != 409 {
					t.Fatalf("round %d approve-first: %s / %s / correction %d %s", round, st, rec, correct.status, correct.body)
				}
			case approve.status == 409 && approve.errCode(t) == "absence_not_eligible": // correction first: nothing was forced
				sawCorrected = true
				if st != "PENDING_TEACHER" || rec != "PRESENT" || correct.status != 200 {
					t.Fatalf("round %d correction-first: %s / %s / correction %d", round, st, rec, correct.status)
				}
			default:
				t.Fatalf("round %d: approve=%d %s correct=%d %s", round, approve.status, approve.body, correct.status, correct.body)
			}
			if n := w.sqlInt(`SELECT count(*) FROM justification_requests j JOIN attendance_records a ON a.id = j.attendance_record_id
				WHERE (j.status = 'JUSTIFIED') <> (a.status = 'JUSTIFIED')`); n != 0 {
				t.Fatalf("round %d: %d out-of-sync pairs", round, n)
			}
		}
		t.Logf("interleavings seen: approval-first=%v correction-first=%v", sawApproved, sawCorrected)
	})
}

// --- rollback with a failing audit row ---------------------------------------------------------------------------

// failingOn fails the audit write of one specific action.
type failingOn struct {
	action string
	real   *auditpg.Recorder
}

func (f failingOn) Record(ctx context.Context, e auditdomain.Event) error {
	if e.Action == f.action || f.action == "*" {
		return errors.New("audit unavailable")
	}
	return f.real.Record(ctx, e)
}

func (j *jfx) serviceWithFailingAudit(action string) *justapp.Service {
	w := j.w
	store, err := juststorage.New(j.storage)
	if err != nil {
		w.t.Fatal(err)
	}
	attSvc := attendanceapp.NewService(attendanceapp.Deps{
		Repo: attendancepg.NewRepository(w.db), Academic: attendancepg.NewAcademicReader(w.db), Tokens: attendancesec.NewQRTokens(),
		Network: attendancenet.New([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, false), Clock: clockFunc(time.Now),
		Audit: auditpg.NewRecorder(w.db), Tx: w.db, NewID: uuid.New,
	})
	return justapp.NewService(justapp.Deps{
		Repo: justpg.NewRepository(w.db), Attendance: justattendance.NewGateway(attSvc), Storage: store,
		Audit: failingOn{action: action, real: auditpg.NewRecorder(w.db)}, Tx: w.db, NewID: uuid.New,
		Log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil)), MaxEvidenceBytes: 1 << 20,
	})
}

func principalOf(w *world, key string, role iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.MustParse(w.ids[key]), SessionID: uuid.New(), Roles: []iddomain.Role{role}}
}

type onceFile struct {
	name string
	data []byte
	done bool
}

func (o *onceFile) Next() (*justapp.Upload, error) {
	if o.done {
		return nil, errEOF
	}
	o.done = true
	return &justapp.Upload{Filename: o.name, Content: bytes.NewReader(o.data)}, nil
}

var errEOF = func() error { _, err := bytes.NewReader(nil).Read(make([]byte, 1)); return err }()

func TestJustificationWritesRollBackWithTheirAuditRow(t *testing.T) {
	ctx := context.Background()

	t.Run("request: no row, no evidence, no file", func(t *testing.T) {
		j := newJFx(t)
		svc := j.serviceWithFailingAudit("justifications.requested")
		_, err := svc.RequestJustification(ctx, principalOf(j.w, "s1", iddomain.RoleStudent), justapp.RequestInput{
			AttendanceRecordID: uuid.MustParse(j.rec1), Reason: "x", Files: &onceFile{name: "a.pdf", data: pdfFile}})
		if err == nil {
			t.Fatal("the request must fail when its audit row cannot be written")
		}
		if j.w.sqlInt(`SELECT count(*) FROM justification_requests`) != 0 || j.w.sqlInt(`SELECT count(*) FROM justification_evidence`) != 0 {
			t.Fatal("database rows survived the rollback")
		}
		if files := j.filesOnDisk(t); len(files) != 0 {
			t.Fatalf("staged file survived: %v", files)
		}
	})

	t.Run("tutor decision", func(t *testing.T) {
		j := newJFx(t)
		id := j.created(t)
		svc := j.serviceWithFailingAudit("justifications.tutor_approved")
		if _, err := svc.DecideAsTutor(ctx, principalOf(j.w, "tutor", iddomain.RoleTutor), uuid.MustParse(id), justapp.DecisionInput{Decision: "APPROVED"}); err == nil {
			t.Fatal("expected a failure")
		}
		if j.status(id) != "PENDING_TUTOR" || j.w.sqlInt(`SELECT count(*) FROM justification_reviews`) != 0 {
			t.Fatal("the tutor's review/status must roll back")
		}
	})

	for _, failing := range []string{"justifications.teacher_approved", "attendance.justified"} {
		t.Run("final approval, audit fails on "+failing, func(t *testing.T) {
			j := newJFx(t)
			id := j.pendingTeacher(t)
			svc := j.serviceWithFailingAudit(failing)
			if _, err := svc.DecideAsTeacher(ctx, principalOf(j.w, "teacher", iddomain.RoleTeacher), uuid.MustParse(id), justapp.DecisionInput{Decision: "APPROVED"}); err == nil {
				t.Fatal("expected a failure")
			}
			// all four pieces roll back together: request, review, attendance and audit rows
			if j.status(id) != "PENDING_TEACHER" {
				t.Fatalf("request status = %s", j.status(id))
			}
			if j.attendanceOf(j.rec1) != "ABSENT" {
				t.Fatalf("attendance = %s: it must never be JUSTIFIED without the whole decision committing", j.attendanceOf(j.rec1))
			}
			if n := j.w.sqlInt(`SELECT count(*) FROM justification_reviews WHERE justification_request_id = $1 AND stage = 'TEACHER'`, id); n != 0 {
				t.Fatalf("teacher review survived: %d", n)
			}
			if n := j.w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action IN ('justifications.teacher_approved', 'attendance.justified')`); n != 0 {
				t.Fatalf("audit rows survived: %d", n)
			}
			// and the decision works afterwards through the normal path
			if r := j.decide("teacher", id, "teacher", "APPROVED", ""); r.status != 200 || j.attendanceOf(j.rec1) != "JUSTIFIED" {
				t.Fatalf("retry: %d %s", r.status, r.body)
			}
		})
	}
}

// --- database constraints ----------------------------------------------------------------------------------------------

func TestJustificationDatabaseConstraints(t *testing.T) {
	j := newJFx(t)
	w := j.w
	ctx := context.Background()
	exec := func(sql string, args ...any) error { _, err := w.db.Q(ctx).Exec(ctx, sql, args...); return err }
	insReq := `INSERT INTO justification_requests (id, attendance_record_id, student_id, reason, status) VALUES ($1, $2, $3, $4, $5)`
	s1 := w.ids["s1"]

	if err := exec(insReq, uuid.New(), uuid.NewString(), s1, "x", "PENDING_TUTOR"); err == nil {
		t.Error("unknown attendance record must violate the FK")
	}
	if err := exec(insReq, uuid.New(), j.rec1, uuid.NewString(), "x", "PENDING_TUTOR"); err == nil {
		t.Error("unknown student must violate the FK")
	}
	for _, reason := range []string{"", "   ", "\n\t"} {
		if err := exec(insReq, uuid.New(), j.rec1, s1, reason, "PENDING_TUTOR"); err == nil {
			t.Errorf("reason %q must be rejected", reason)
		}
	}
	if err := exec(insReq, uuid.New(), j.rec1, s1, "x", "OPEN"); err == nil {
		t.Error("unknown status must be rejected")
	}

	// the partial unique index: one open/approved request per absence, rejected ones don't count
	a := uuid.New()
	if err := exec(insReq, a, j.rec1, s1, "x", "PENDING_TUTOR"); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"PENDING_TUTOR", "PENDING_TEACHER", "JUSTIFIED"} {
		if err := exec(insReq, uuid.New(), j.rec1, s1, "x", st); err == nil {
			t.Errorf("a second %s request for the same absence must be rejected", st)
		}
	}
	if err := exec(`UPDATE justification_requests SET status = 'TUTOR_REJECTED' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"TUTOR_REJECTED", "TEACHER_REJECTED", "PENDING_TUTOR"} {
		if err := exec(insReq, uuid.New(), j.rec1, s1, "x", st); err != nil {
			t.Errorf("after a rejection a %s row must be allowed: %v", st, err)
		}
		_ = exec(`UPDATE justification_requests SET status = 'TUTOR_REJECTED' WHERE attendance_record_id = $1 AND status = 'PENDING_TUTOR'`, j.rec1)
	}

	// evidence
	req := w.sqlString(`SELECT id::text FROM justification_requests LIMIT 1`)
	insEv := `INSERT INTO justification_evidence (id, justification_request_id, storage_key, original_filename, mime_type, size_bytes) VALUES ($1, $2, $3, 'a', $4, $5)`
	for name, c := range map[string]struct {
		req, key, mime string
		size           int
	}{
		"unknown request":  {uuid.NewString(), "k1", "application/pdf", 10},
		"mime not allowed": {req, "k2", "text/html", 10},
		"exe mime":         {req, "k3", "application/x-msdownload", 10},
		"zero size":        {req, "k4", "application/pdf", 0},
		"above 10 MiB":     {req, "k5", "application/pdf", 10<<20 + 1},
	} {
		if err := exec(insEv, uuid.New(), c.req, c.key, c.mime, c.size); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if err := exec(insEv, uuid.New(), req, "same-key", "application/pdf", 10<<20); err != nil {
		t.Fatalf("10 MiB exactly is allowed: %v", err)
	}
	if err := exec(insEv, uuid.New(), req, "same-key", "image/png", 1); err == nil {
		t.Error("storage_key must be unique")
	}

	// reviews: FK, enums, one per stage, comment on rejection, append-only
	insRv := `INSERT INTO justification_reviews (id, justification_request_id, reviewer_id, stage, decision, comment) VALUES ($1, $2, $3, $4, $5, $6)`
	tutor := w.ids["tutor"]
	if err := exec(insRv, uuid.New(), uuid.NewString(), tutor, "TUTOR", "APPROVED", nil); err == nil {
		t.Error("unknown request must violate the FK")
	}
	if err := exec(insRv, uuid.New(), req, uuid.NewString(), "TUTOR", "APPROVED", nil); err == nil {
		t.Error("unknown reviewer must violate the FK")
	}
	if err := exec(insRv, uuid.New(), req, tutor, "DIRECTOR", "APPROVED", nil); err == nil {
		t.Error("unknown stage must be rejected")
	}
	if err := exec(insRv, uuid.New(), req, tutor, "TUTOR", "MAYBE", nil); err == nil {
		t.Error("unknown decision must be rejected")
	}
	for _, c := range []any{nil, "", "   "} {
		if err := exec(insRv, uuid.New(), req, tutor, "TUTOR", "REJECTED", c); err == nil {
			t.Errorf("a rejection with comment %v must be refused by the database", c)
		}
	}
	rv := uuid.New()
	if err := exec(insRv, rv, req, tutor, "TUTOR", "APPROVED", nil); err != nil {
		t.Fatalf("an approval needs no comment: %v", err)
	}
	if err := exec(insRv, uuid.New(), req, tutor, "TUTOR", "REJECTED", "otra vez"); err == nil {
		t.Error("a stage can be decided only once per request")
	}
	if err := exec(insRv, uuid.New(), req, w.ids["teacher"], "TEACHER", "REJECTED", "no"); err != nil {
		t.Errorf("the other stage is independent: %v", err)
	}
	for name, sql := range map[string]string{
		"UPDATE": `UPDATE justification_reviews SET decision = 'REJECTED', comment = 'x' WHERE id = $1`,
		"DELETE": `DELETE FROM justification_reviews WHERE id = $1`,
	} {
		if err := exec(sql, rv); err == nil {
			t.Errorf("%s of a review must be refused: the history is append-only", name)
		}
	}
	if err := exec(`DELETE FROM justification_requests WHERE id = $1`, req); err == nil {
		t.Error("a request with a history cannot be deleted")
	}
	if got := w.sqlString(`SELECT decision FROM justification_reviews WHERE id = $1`, rv); got != "APPROVED" {
		t.Fatalf("review mutated to %s", got)
	}
}

// downloadHeaders performs an authorized GET and returns the response headers.
func downloadHeaders(w *world, who, path string) http.Header {
	w.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "192.0.2.10:4321"
	req.Header.Set("Authorization", "Bearer "+w.token[who])
	rec := httptest.NewRecorder()
	w.c.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		w.t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Header()
}
