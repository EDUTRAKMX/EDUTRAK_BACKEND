package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewNote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
		out  string
	}{
		{"valid", "  Acordamos asistir a asesoria  ", nil, "Acordamos asistir a asesoria"},
		{"empty", "", ErrNoteRequired, ""},
		{"spaces only", "  \t\n ", ErrNoteRequired, ""},
		{"max length", strings.Repeat("a", MaxNoteLength), nil, strings.Repeat("a", MaxNoteLength)},
		{"too long", strings.Repeat("a", MaxNoteLength+1), ErrNoteTooLong, ""},
		{"counts characters not bytes", strings.Repeat("ñ", MaxNoteLength), nil, strings.Repeat("ñ", MaxNoteLength)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewNote(c.in)
			if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if got != c.out {
				t.Fatalf("note = %q", got)
			}
		})
	}
}

func day(s string) time.Time {
	d, err := time.Parse(DateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestParseDate(t *testing.T) {
	for _, bad := range []string{"", "2026-13-01", "2026-02-30", "01/02/2026", "2026-1-5", "2026-02-01T10:00:00Z", "hoy"} {
		if _, err := ParseDate(bad); !errors.Is(err, ErrInvalidDate) {
			t.Errorf("ParseDate(%q) = %v, want invalid_followup_date", bad, err)
		}
	}
	if d, err := ParseDate("2026-02-28"); err != nil || d != day("2026-02-28") {
		t.Fatalf("valid date: %v %v", d, err)
	}
}

func TestCheckFollowUpDate(t *testing.T) {
	today, start := day("2026-03-10"), day("2026-01-05")
	cases := []struct {
		date string
		want error
	}{
		{"2026-03-10", nil}, // today
		{"2026-01-05", nil}, // first day of the term
		{"2026-02-01", nil},
		{"2026-03-11", ErrDateInFuture},
		{"2026-01-04", ErrDateBeforeTerm},
	}
	for _, c := range cases {
		if err := CheckFollowUpDate(day(c.date), today, start); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: err = %v, want %v", c.date, err, c.want)
		}
	}
}

func TestNewFollowUpKeepsAuthorAndTrimsNote(t *testing.T) {
	id, student, tutor, group := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f, err := New(id, student, tutor, group, day("2026-02-01"), "  nota  ")
	if err != nil {
		t.Fatal(err)
	}
	if f.TutorID != tutor || f.StudentID != student || f.GroupID != group || f.Note != "nota" {
		t.Fatalf("unexpected follow-up: %+v", f)
	}
	if _, err := New(id, student, tutor, group, day("2026-02-01"), "   "); !errors.Is(err, ErrNoteRequired) {
		t.Fatalf("blank note accepted: %v", err)
	}
}

func TestEnrollmentRequireWritable(t *testing.T) {
	cases := []struct {
		term, enr string
		want      error
	}{
		{"active", "ACTIVE", nil},
		{"draft", "ACTIVE", nil},
		{"closed", "ACTIVE", ErrTermClosed},
		{"active", "WITHDRAWN", ErrEnrollmentInactive},
		{"closed", "WITHDRAWN", ErrTermClosed},
	}
	for _, c := range cases {
		e := Enrollment{TermStatus: c.term, EnrollmentStatus: c.enr}
		if err := e.RequireWritable(); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s/%s: %v, want %v", c.term, c.enr, err, c.want)
		}
	}
}

func TestUnitGradeScoreDistinguishesPendingFromZero(t *testing.T) {
	if _, _, ok := (UnitGrade{}).Score(); ok {
		t.Fatal("a unit without a grade must not report a score")
	}
	h := func(v int32) *int32 { return &v }
	cases := []struct {
		hundredths int32
		score, cat string
	}{
		{0, "0.00", "NA"}, // an explicit zero is a grade; pending is something else
		{7999, "79.99", "NA"},
		{8000, "80.00", "SA"},
		{8999, "89.99", "SA"},
		{9000, "90.00", "DE"},
		{9999, "99.99", "DE"},
		{10000, "100.00", "AU"},
	}
	for _, c := range cases {
		score, cat, ok := (UnitGrade{Hundredths: h(c.hundredths)}).Score()
		if !ok || score != c.score || cat != c.cat {
			t.Errorf("%d -> %q %q %v, want %q %q", c.hundredths, score, cat, ok, c.score, c.cat)
		}
	}
}

func TestAttendanceIndicators(t *testing.T) {
	three := 3
	total, fromLates, effective := Attendance{Present: 5, Late: 7, Absent: 2, Justified: 1, LatesPerAbsence: &three}.Indicators()
	if total != 15 || fromLates != 2 || effective != 4 {
		t.Fatalf("got total=%d fromLates=%d effective=%d", total, fromLates, effective)
	}
	// Without sessions there is no configured equivalence: lates never turn into absences.
	total, fromLates, effective = Attendance{Late: 7, Absent: 2}.Indicators()
	if total != 9 || fromLates != 0 || effective != 2 {
		t.Fatalf("without equivalence: total=%d fromLates=%d effective=%d", total, fromLates, effective)
	}
}
