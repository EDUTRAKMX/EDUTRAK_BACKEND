package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNewTerm(t *testing.T) {
	tests := []struct {
		name       string
		termName   string
		start, end string
		wantErr    bool
		wantIs     error
	}{
		{"valid", "2026-1", "2026-01-05", "2026-04-30", false, nil},
		{"start after end", "2026-1", "2026-05-01", "2026-04-30", true, ErrInvalidDates},
		{"same day", "2026-1", "2026-01-05", "2026-01-05", true, ErrInvalidDates},
		{"blank name", "   ", "2026-01-05", "2026-04-30", true, nil},
		{"name too long", strings.Repeat("x", 81), "2026-01-05", "2026-04-30", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, err := NewTerm(uuid.New(), tt.termName, date(tt.start), date(tt.end))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("err = %v, want %v", err, tt.wantIs)
			}
			if err == nil && term.Status != TermDraft {
				t.Fatalf("new term status = %s, want draft", term.Status)
			}
		})
	}
}

func TestTermTransitions(t *testing.T) {
	tests := []struct {
		from, to TermStatus
		ok       bool
	}{
		{TermDraft, TermActive, true},
		{TermActive, TermClosed, true},
		{TermDraft, TermClosed, false},
		{TermActive, TermActive, false},
		{TermActive, TermDraft, false},
		{TermClosed, TermActive, false},
		{TermClosed, TermDraft, false},
		{TermClosed, TermClosed, false},
	}
	for _, tt := range tests {
		got, err := (&Term{Status: tt.from}).Transition(tt.to)
		if tt.ok && (err != nil || got != tt.to) {
			t.Errorf("%s -> %s: got %q, %v; want allowed", tt.from, tt.to, got, err)
		}
		if !tt.ok && !errors.Is(err, ErrInvalidTermTransition) {
			t.Errorf("%s -> %s: err = %v, want ErrInvalidTermTransition", tt.from, tt.to, err)
		}
	}
	if err := (&Term{Status: TermClosed}).RequireOpen(); !errors.Is(err, ErrTermClosed) {
		t.Fatalf("closed term RequireOpen = %v", err)
	}
	for _, s := range []TermStatus{TermDraft, TermActive} {
		if err := (&Term{Status: s}).RequireOpen(); err != nil {
			t.Fatalf("%s term RequireOpen = %v", s, err)
		}
	}
}

func TestNewSchedule(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name       string
		day        int
		start, end string
		want       error
	}{
		{"valid", 1, "07:00", "08:40", nil},
		{"sunday", 7, "10:00", "11:00", nil},
		{"day zero", 0, "07:00", "08:00", ErrInvalidDay},
		{"day eight", 8, "07:00", "08:00", ErrInvalidDay},
		{"start equals end", 2, "07:00", "07:00", ErrInvalidTimeRange},
		{"start after end", 2, "09:00", "08:00", ErrInvalidTimeRange},
		{"bad start", 2, "7am", "08:00", ErrInvalidTime},
		{"bad hour", 2, "25:00", "26:00", ErrInvalidTime},
		{"no leading zero", 2, "7:00", "08:00", ErrInvalidTime},
		{"seconds not accepted", 2, "07:00:00", "08:00", ErrInvalidTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSchedule(uuid.New(), id, tt.day, tt.start, tt.end, "")
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := NewSchedule(uuid.New(), id, 1, "07:00", "08:00", strings.Repeat("x", 51)); err == nil {
		t.Fatal("classroom longer than 50 must be rejected")
	}
	s, _ := NewSchedule(uuid.New(), id, 1, "07:00", "08:00", "  Aula 3  ")
	if s.Classroom != "Aula 3" {
		t.Fatalf("classroom = %q, want trimmed", s.Classroom)
	}
}

func TestScheduleOverlaps(t *testing.T) {
	mk := func(day int16, s, e string) Schedule { return Schedule{DayOfWeek: day, StartTime: s, EndTime: e} }
	base := mk(1, "08:00", "10:00")
	tests := []struct {
		name string
		o    Schedule
		want bool
	}{
		{"same slot", mk(1, "08:00", "10:00"), true},
		{"partial", mk(1, "09:00", "11:00"), true},
		{"contained", mk(1, "08:30", "09:00"), true},
		{"back to back before", mk(1, "07:00", "08:00"), false},
		{"back to back after", mk(1, "10:00", "11:00"), false},
		{"other day", mk(2, "08:00", "10:00"), false},
	}
	for _, tt := range tests {
		if got := base.Overlaps(tt.o); got != tt.want {
			t.Errorf("%s: Overlaps = %v, want %v", tt.name, got, tt.want)
		}
		if got := tt.o.Overlaps(base); got != tt.want {
			t.Errorf("%s (reversed): Overlaps = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCodesLevelsAndNames(t *testing.T) {
	if c, err := NewCode("  tic-01 "); err != nil || c != "TIC-01" {
		t.Fatalf("NewCode = %q, %v", c, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("A", 31)} {
		if _, err := NewCode(bad); err == nil {
			t.Errorf("NewCode(%q) should fail", bad)
		}
	}
	for _, l := range []int{1, 8, 32767} {
		if err := ValidateLevel(l); err != nil {
			t.Errorf("level %d should be valid: %v", l, err)
		}
	}
	for _, l := range []int{0, -1, 32768} {
		if err := ValidateLevel(l); !errors.Is(err, ErrInvalidLevel) {
			t.Errorf("level %d err = %v, want ErrInvalidLevel", l, err)
		}
	}
	if _, err := NewCareerName(strings.Repeat("n", 181)); err == nil {
		t.Error("career name over 180 chars must fail")
	}
	if err := ValidateUnitNumber(0); err == nil {
		t.Error("unit 0 must fail")
	}
}
