package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var opened = time.Date(2026, 2, 2, 8, 0, 0, 0, time.UTC)

func newSession(t *testing.T, lateAfter, lates int) *Session {
	t.Helper()
	s, err := NewSession(uuid.New(), uuid.New(), nil, uuid.New(), opened, opened.Truncate(24*time.Hour), lateAfter, lates)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSessionValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		late, lates int
		want        error
	}{
		{"defaults", 15, 3, nil},
		{"minimum", 1, 1, nil},
		{"maximums", MaxLateAfterMinutes, MaxLatesPerAbsence, nil},
		{"zero minutes", 0, 3, ErrInvalidLateAfter},
		{"negative minutes", -5, 3, ErrInvalidLateAfter},
		{"too many minutes", MaxLateAfterMinutes + 1, 3, ErrInvalidLateAfter},
		{"zero lates", 15, 0, ErrInvalidLatesPerAbs},
		{"negative lates", 15, -1, ErrInvalidLatesPerAbs},
		{"too many lates", 15, MaxLatesPerAbsence + 1, ErrInvalidLatesPerAbs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSession(uuid.New(), uuid.New(), nil, uuid.New(), opened, opened, tt.late, tt.lates)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if err == nil && s.Status != SessionOpen {
				t.Fatalf("new session status = %s, want OPEN", s.Status)
			}
		})
	}
}

func TestSessionStateTransitions(t *testing.T) {
	s := newSession(t, 15, 3)
	if err := s.RequireOpen(); err != nil {
		t.Fatal(err)
	}
	s.Status = SessionClosed
	if err := s.RequireOpen(); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed session RequireOpen = %v", err)
	}
}

func TestClassifyUsesTheOpeningAsReference(t *testing.T) {
	s := newSession(t, 15, 3)
	tests := []struct {
		name  string
		after time.Duration
		want  RecordStatus
	}{
		{"at opening", 0, StatusPresent},
		{"one minute", time.Minute, StatusPresent},
		{"14:59", 14*time.Minute + 59*time.Second, StatusPresent},
		{"14:59.999", 14*time.Minute + 59*time.Second + 999*time.Millisecond, StatusPresent},
		{"exactly 15:00 is late", 15 * time.Minute, StatusLate},
		{"15:01", 15*time.Minute + time.Second, StatusLate},
		{"hours later", 3 * time.Hour, StatusLate},
		{"clock skew before opening", -time.Second, StatusPresent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Classify(opened.Add(tt.after)); got != tt.want {
				t.Fatalf("Classify(+%v) = %s, want %s", tt.after, got, tt.want)
			}
		})
	}
	// the threshold is configurable per session
	strict := newSession(t, 5, 3)
	if strict.Classify(opened.Add(4*time.Minute)) != StatusPresent || strict.Classify(opened.Add(5*time.Minute)) != StatusLate {
		t.Fatal("a 5-minute threshold must flip at exactly 5 minutes")
	}
}

func TestCheckQR(t *testing.T) {
	s := newSession(t, 15, 3)
	exp := opened.Add(30 * time.Second)
	s.QRTokenHash, s.QRExpiresAt = "abc123", &exp

	tests := []struct {
		name string
		hash string
		now  time.Time
		want error
	}{
		{"valid", "abc123", opened.Add(10 * time.Second), nil},
		{"valid just before expiry", "abc123", exp.Add(-time.Nanosecond), nil},
		{"expired exactly at expiry", "abc123", exp, ErrQRExpired},
		{"expired later", "abc123", exp.Add(time.Hour), ErrQRExpired},
		{"wrong token", "zzz", opened, ErrInvalidQR},
		{"empty token", "", opened, ErrInvalidQR},
		{"prefix of the token", "abc12", opened, ErrInvalidQR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.CheckQR(tt.hash, tt.now); !errors.Is(err, tt.want) && !(tt.want == nil && err == nil) {
				t.Fatalf("CheckQR = %v, want %v", err, tt.want)
			}
		})
	}
	// no QR issued yet / rotated away / closed session
	none := newSession(t, 15, 3)
	if err := none.CheckQR("abc123", opened); !errors.Is(err, ErrInvalidQR) {
		t.Fatalf("no active QR: %v", err)
	}
	s.Status = SessionClosed
	if err := s.CheckQR("abc123", opened); !errors.Is(err, ErrInvalidQR) {
		t.Fatalf("closed session must not accept a QR: %v", err)
	}
}

func TestParseWritableStatus(t *testing.T) {
	for _, ok := range []string{"PRESENT", "LATE", "ABSENT"} {
		if st, err := ParseWritableStatus(ok); err != nil || string(st) != ok {
			t.Errorf("%s: %v %v", ok, st, err)
		}
	}
	for _, bad := range []string{"JUSTIFIED", "present", "", "QR", "MANUAL", "NFC", "OPEN", "CLOSED"} {
		if _, err := ParseWritableStatus(bad); !errors.Is(err, ErrStatusNotAllowed) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
}

func TestLatesToAbsenceIsAnIndicatorNotARewrite(t *testing.T) {
	tests := []struct {
		c                    Counts
		per                  int
		fromLates, effective int
	}{
		{Counts{Late: 0}, 3, 0, 0},
		{Counts{Late: 2}, 3, 0, 0},
		{Counts{Late: 3}, 3, 1, 1},
		{Counts{Late: 5}, 3, 1, 1},
		{Counts{Late: 6}, 3, 2, 2},
		{Counts{Late: 7, Absent: 2}, 3, 2, 4},
		{Counts{Late: 4, Justified: 5, Present: 10}, 2, 2, 2}, // justified never count as absences
		{Counts{Late: 3, Absent: 1}, 0, 0, 1},                 // invalid divisor degrades safely
	}
	for _, tt := range tests {
		if got := tt.c.AbsencesFromLates(tt.per); got != tt.fromLates {
			t.Errorf("%+v /%d AbsencesFromLates = %d, want %d", tt.c, tt.per, got, tt.fromLates)
		}
		if got := tt.c.EffectiveAbsences(tt.per); got != tt.effective {
			t.Errorf("%+v /%d EffectiveAbsences = %d, want %d", tt.c, tt.per, got, tt.effective)
		}
	}
	c := Counts{Present: 1, Late: 3, Absent: 1, Justified: 1}
	_ = c.EffectiveAbsences(3)
	if c.Late != 3 || c.Total() != 6 {
		t.Fatal("computing indicators must not change the counts")
	}
}

func TestAssignmentAndEnrollmentGuards(t *testing.T) {
	if err := (AssignmentRef{TermStatus: "closed"}).RequireOpenTerm(); !errors.Is(err, ErrTermClosed) {
		t.Fatal(err)
	}
	for _, st := range []string{"draft", "active"} {
		if err := (AssignmentRef{TermStatus: st}).RequireOpenTerm(); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	if err := (EnrollmentRef{Status: "WITHDRAWN"}).RequireActive(); !errors.Is(err, ErrEnrollmentInactive) {
		t.Fatal(err)
	}
	if err := (EnrollmentRef{Status: "ACTIVE"}).RequireActive(); err != nil {
		t.Fatal(err)
	}
}
