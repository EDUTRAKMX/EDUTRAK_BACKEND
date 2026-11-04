package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestInitialState(t *testing.T) {
	j, err := New(uuid.New(), uuid.New(), uuid.New(), "  Consulta médica  ")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != StatusPendingTutor || j.Reason != "Consulta médica" {
		t.Fatalf("new request = %+v", j)
	}
	if st, ok := j.Status.AwaitedStage(); !ok || st != StageTutor {
		t.Fatal("a new request waits for the tutor")
	}
}

func TestReasonValidation(t *testing.T) {
	for _, bad := range []string{"", "   ", "\n\t"} {
		if _, err := NewReason(bad); !errors.Is(err, ErrReasonRequired) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := NewReason(strings.Repeat("a", MaxReasonLen+1)); !errors.Is(err, ErrReasonTooLong) {
		t.Fatal("too long reason must fail")
	}
	if _, err := NewReason(strings.Repeat("ñ", MaxReasonLen)); err != nil {
		t.Fatalf("the limit counts characters, not bytes: %v", err)
	}
}

func TestStateMachineTransitions(t *testing.T) {
	tests := []struct {
		from  Status
		stage Stage
		d     Decision
		want  Status
		err   error
	}{
		// the valid paths
		{StatusPendingTutor, StageTutor, DecisionApproved, StatusPendingTeacher, nil},
		{StatusPendingTutor, StageTutor, DecisionRejected, StatusTutorRejected, nil},
		{StatusPendingTeacher, StageTeacher, DecisionApproved, StatusJustified, nil},
		{StatusPendingTeacher, StageTeacher, DecisionRejected, StatusTeacherRejected, nil},
		// the teacher can never decide before the tutor, nor the tutor take the teacher's decision
		{StatusPendingTutor, StageTeacher, DecisionApproved, "", ErrWrongStage},
		{StatusPendingTutor, StageTeacher, DecisionRejected, "", ErrWrongStage},
		{StatusPendingTeacher, StageTutor, DecisionApproved, "", ErrWrongStage},
		{StatusPendingTeacher, StageTutor, DecisionRejected, "", ErrWrongStage},
		// finished requests are immutable
		{StatusTutorRejected, StageTutor, DecisionApproved, "", ErrFinalized},
		{StatusTutorRejected, StageTeacher, DecisionApproved, "", ErrFinalized},
		{StatusTeacherRejected, StageTeacher, DecisionApproved, "", ErrFinalized},
		{StatusTeacherRejected, StageTutor, DecisionRejected, "", ErrFinalized},
		{StatusJustified, StageTeacher, DecisionRejected, "", ErrFinalized},
		{StatusJustified, StageTeacher, DecisionApproved, "", ErrFinalized},
		{StatusJustified, StageTutor, DecisionApproved, "", ErrFinalized},
		// unknown decision / status
		{StatusPendingTutor, StageTutor, Decision("MAYBE"), "", ErrInvalidDecision},
		{Status("BOGUS"), StageTutor, DecisionApproved, "", ErrWrongStage},
	}
	for _, tt := range tests {
		got, err := tt.from.Next(tt.stage, tt.d)
		if tt.err == nil && (err != nil || got != tt.want) || tt.err != nil && !errors.Is(err, tt.err) {
			t.Errorf("%s + %s %s = %q, %v; want %q, %v", tt.from, tt.stage, tt.d, got, err, tt.want, tt.err)
		}
	}
}

func TestStageCannotBeDecidedTwice(t *testing.T) {
	s, err := StatusPendingTutor.Next(StageTutor, DecisionApproved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(StageTutor, DecisionApproved); !errors.Is(err, ErrWrongStage) {
		t.Fatalf("a second tutor decision: %v", err)
	}
	final, err := s.Next(StageTeacher, DecisionApproved)
	if err != nil || final != StatusJustified {
		t.Fatal("tutor then teacher must end JUSTIFIED")
	}
	if _, err := final.Next(StageTeacher, DecisionApproved); !errors.Is(err, ErrFinalized) {
		t.Fatalf("a second teacher decision: %v", err)
	}
}

func TestTerminalStatusesAndAwaitedStage(t *testing.T) {
	for _, s := range []Status{StatusTutorRejected, StatusTeacherRejected, StatusJustified} {
		if !s.Terminal() {
			t.Errorf("%s must be terminal", s)
		}
		if _, waiting := s.AwaitedStage(); waiting {
			t.Errorf("%s waits for nobody", s)
		}
	}
	for _, s := range []Status{StatusPendingTutor, StatusPendingTeacher} {
		if s.Terminal() {
			t.Errorf("%s is not terminal", s)
		}
	}
	if st, _ := StatusPendingTeacher.AwaitedStage(); st != StageTeacher {
		t.Fatal("PENDING_TEACHER waits for the teacher")
	}
}

func TestParsers(t *testing.T) {
	if _, err := ParseDecision("APPROVED"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "approved", "YES", "JUSTIFIED"} {
		if _, err := ParseDecision(bad); !errors.Is(err, ErrInvalidDecision) {
			t.Errorf("decision %q: %v", bad, err)
		}
	}
	for _, ok := range []string{"PENDING_TUTOR", "TUTOR_REJECTED", "PENDING_TEACHER", "TEACHER_REJECTED", "JUSTIFIED"} {
		if _, err := ParseStatus(ok); err != nil {
			t.Errorf("status %s: %v", ok, err)
		}
	}
	if _, err := ParseStatus("OPEN"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatal("unknown status must fail")
	}
}

func TestReviewRules(t *testing.T) {
	id, req, rev := uuid.New(), uuid.New(), uuid.New()
	if _, err := NewReview(id, req, rev, StageTutor, DecisionRejected, "  "); !errors.Is(err, ErrCommentRequired) {
		t.Fatalf("a rejection needs a comment: %v", err)
	}
	if r, err := NewReview(id, req, rev, StageTutor, DecisionRejected, " Sin sustento "); err != nil || r.Comment != "Sin sustento" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := NewReview(id, req, rev, StageTeacher, DecisionApproved, ""); err != nil {
		t.Fatalf("an approval may have no comment: %v", err)
	}
	if _, err := NewReview(id, req, rev, StageTeacher, DecisionApproved, strings.Repeat("x", MaxCommentLen+1)); !errors.Is(err, ErrCommentTooLong) {
		t.Fatal("comment too long must fail")
	}
}
