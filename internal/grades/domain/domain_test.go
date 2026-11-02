package domain

import (
	"errors"
	"testing"
)

func TestClassificationBoundaries(t *testing.T) {
	tests := []struct {
		in   string
		want Category
	}{
		{"0.00", CategoryNA},
		{"0", CategoryNA},
		{"0.01", CategoryNA},
		{"50", CategoryNA},
		{"79.99", CategoryNA},
		{"80.00", CategorySA},
		{"80", CategorySA},
		{"85.5", CategorySA},
		{"89.99", CategorySA},
		{"90.00", CategoryDE},
		{"90", CategoryDE},
		{"95.01", CategoryDE},
		{"99.99", CategoryDE},
		{"100.00", CategoryAU},
		{"100", CategoryAU},
		{"100.0", CategoryAU},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			s, err := ParseScore(tt.in)
			if err != nil {
				t.Fatalf("ParseScore(%q): %v", tt.in, err)
			}
			if got := s.Category(); got != tt.want {
				t.Fatalf("%s -> %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseScoreRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		in   string
		want error
	}{
		{"-0.01", ErrScoreRange},
		{"-1", ErrScoreRange},
		{"-0", ErrScoreRange},
		{"100.01", ErrScoreRange},
		{"101", ErrScoreRange},
		{"1000", ErrScoreRange},
		{"89.999", ErrScorePrecision}, // never rounded silently
		{"79.995", ErrScorePrecision},
		{"99.991", ErrScorePrecision},
		{"0.001", ErrScorePrecision},
		{"", ErrInvalidScore},
		{"abc", ErrInvalidScore},
		{"1e2", ErrInvalidScore},
		{"+90", ErrInvalidScore},
		{".5", ErrInvalidScore},
		{"5.", ErrInvalidScore},
		{"9 0", ErrInvalidScore},
		{"90,5", ErrInvalidScore},
		{"NaN", ErrInvalidScore},
		{"Infinity", ErrInvalidScore},
		{"--5", ErrInvalidScore},
		{"1234567", ErrInvalidScore},
		{"90.", ErrInvalidScore},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if _, err := ParseScore(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("ParseScore(%q) err = %v, want %v", tt.in, err, tt.want)
			}
		})
	}
}

func TestScoreIsExactAndKeepsTwoDecimals(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0", "0.00"}, {"0.0", "0.00"}, {"5", "5.00"}, {"79.99", "79.99"}, {"89.9", "89.90"},
		{"90.000", "90.00"}, // extra trailing zeros are the same value, not a rounding
		{"100", "100.00"}, {"0.10", "0.10"}, {"0.07", "0.07"},
	}
	for _, tt := range tests {
		s, err := ParseScore(tt.in)
		if err != nil {
			t.Fatalf("ParseScore(%q): %v", tt.in, err)
		}
		if s.String() != tt.want {
			t.Errorf("%q -> %q, want %q", tt.in, s.String(), tt.want)
		}
	}
	// 0.1 + 0.2 style traps: 0.30 must be exactly 30 hundredths, and ordered correctly.
	a, _ := ParseScore("0.30")
	if a.Hundredths() != 30 {
		t.Fatalf("0.30 = %d hundredths", a.Hundredths())
	}
	x, _ := ParseScore("79.99")
	y, _ := ParseScore("80.00")
	if !(x.Hundredths() < y.Hundredths()) || x.Category() == y.Category() {
		t.Fatal("79.99 and 80.00 must be different, ordered categories")
	}
}

func TestZeroIsARealScoreNotAPendingOne(t *testing.T) {
	var zero Score
	if zero.String() != "0.00" || zero.Category() != CategoryNA {
		t.Fatalf("zero score = %s / %s", zero.String(), zero.Category())
	}
	// "pending" is represented by a nil pointer, which is distinguishable from zero.
	var pending *Score
	if pending != nil {
		t.Fatal("pending must be nil")
	}
	if got := (UnitGrade{Score: &zero}).Score; got == nil {
		t.Fatal("0.00 must be a captured value")
	}
}

func TestNewScoreFromHundredths(t *testing.T) {
	for _, h := range []int64{0, 1, 7999, 8000, 10000} {
		if _, err := NewScoreFromHundredths(h); err != nil {
			t.Errorf("%d should be valid: %v", h, err)
		}
	}
	for _, h := range []int64{-1, 10001} {
		if _, err := NewScoreFromHundredths(h); !errors.Is(err, ErrScoreRange) {
			t.Errorf("%d err = %v, want range error", h, err)
		}
	}
}

func TestRequireWritable(t *testing.T) {
	ok := SubjectEnrollmentRef{TermStatus: "active", Status: EnrollmentStatusActive}
	if err := ok.RequireWritable(); err != nil {
		t.Fatal(err)
	}
	draft := SubjectEnrollmentRef{TermStatus: "draft", Status: EnrollmentStatusActive}
	if err := draft.RequireWritable(); err != nil {
		t.Fatalf("draft term must allow grading: %v", err)
	}
	closed := SubjectEnrollmentRef{TermStatus: "closed", Status: EnrollmentStatusActive}
	if err := closed.RequireWritable(); !errors.Is(err, ErrTermClosed) {
		t.Fatalf("closed err = %v", err)
	}
	withdrawn := SubjectEnrollmentRef{TermStatus: "active", Status: "WITHDRAWN"}
	if err := withdrawn.RequireWritable(); !errors.Is(err, ErrEnrollmentNotActive) {
		t.Fatalf("withdrawn err = %v", err)
	}
}
