package application

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// RecalcResult says what one evaluation changed.
type RecalcResult struct {
	GroupID     uuid.UUID
	StudentID   *uuid.UUID
	Created     int
	Updated     int
	Resolved    int
	Unchanged   int
	EvaluatedAt string
	Open        map[domain.Rule]int
}

type alertKey struct {
	student uuid.UUID
	rule    domain.Rule
	key     string
}

// sameJSON compares two JSON documents by value (key order and spacing do not matter).
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// RecalculateAlerts re-evaluates the alert rules for ONE group (optionally one of its students)
// against the current data, in one transaction:
//
//   - a condition that holds and has no unresolved alert creates one (OPEN);
//   - a condition that holds and already has one keeps it (an ACKNOWLEDGED alert stays
//     acknowledged), refreshing message/evidence only if they changed;
//   - an unresolved alert whose condition no longer holds becomes RESOLVED.
//
// Conditions are identified by (student, rule, condition key), so running it twice produces the
// same state with no duplicates; a partial unique index backs this up, and an advisory lock per
// group serializes concurrent evaluations. Only DIRECTOR or the current tutor of the group may run
// it, never a student, a teacher or ADMIN. A closed term is frozen (409). One audit event is
// written per run that changed something (never one per alert, never for a run with no changes).
func (s *Service) RecalculateAlerts(ctx context.Context, p iddomain.Principal, groupID uuid.UUID, studentID *uuid.UUID) (*RecalcResult, error) {
	if _, err := scopeOf(p); err != nil {
		return nil, err
	}
	var res *RecalcResult
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Alerts.LockGroup(ctx, groupID); err != nil {
			return err
		}
		g, err := s.authorizeGroup(ctx, p, groupID, true)
		if err != nil {
			return err
		}
		if g.TermStatus == "closed" {
			return domain.ErrTermClosed
		}
		if studentID != nil {
			if _, err := s.Facts.Enrollment(ctx, *studentID, groupID); err != nil {
				return err
			}
		}
		students, err := s.Facts.Students(ctx, groupID, studentID)
		if err != nil {
			return err
		}
		desired := map[alertKey]domain.Candidate{}
		var order []alertKey
		for _, st := range students {
			for _, c := range domain.Evaluate(*g, st, s.Rules) {
				k := alertKey{c.StudentID, c.Rule, c.Key}
				desired[k] = c
				order = append(order, k)
			}
		}
		sort.SliceStable(order, func(i, j int) bool { // deterministic insertion order
			if order[i].student != order[j].student {
				return order[i].student.String() < order[j].student.String()
			}
			if order[i].rule != order[j].rule {
				return order[i].rule < order[j].rule
			}
			return order[i].key < order[j].key
		})

		open, err := s.Alerts.Unresolved(ctx, groupID, studentID)
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		res = &RecalcResult{GroupID: groupID, StudentID: studentID, EvaluatedAt: now.UTC().Format("2006-01-02T15:04:05Z")}
		existing := map[alertKey]domain.StoredAlert{}
		for _, a := range open {
			existing[alertKey{a.StudentID, a.Rule, a.Key}] = a
		}

		var touch, resolve []uuid.UUID
		for _, k := range order {
			c := desired[k]
			ev, err := json.Marshal(c.Evidence)
			if err != nil {
				return err
			}
			cur, found := existing[k]
			switch {
			case !found:
				if err := s.Alerts.Create(ctx, ports.NewAlert{ID: s.NewID(), GroupID: groupID, Candidate: c, Evidence: ev, Now: now}); err != nil {
					return err
				}
				res.Created++
			case cur.Message != c.Message || !sameJSON(cur.Evidence, ev):
				if err := s.Alerts.Update(ctx, cur.ID, c.Message, ev, now); err != nil {
					return err
				}
				res.Updated++
			default:
				touch = append(touch, cur.ID)
				res.Unchanged++
			}
		}
		for k, a := range existing {
			if _, still := desired[k]; !still {
				resolve = append(resolve, a.ID)
			}
		}
		sort.Slice(resolve, func(i, j int) bool { return resolve[i].String() < resolve[j].String() })
		if err := s.Alerts.Touch(ctx, touch, now); err != nil {
			return err
		}
		if err := s.Alerts.Resolve(ctx, resolve, now); err != nil {
			return err
		}
		res.Resolved = len(resolve)

		if res.Created+res.Updated+res.Resolved > 0 {
			actor := p.UserID
			meta := map[string]any{"group_id": groupID.String(), "created": res.Created, "updated": res.Updated, "resolved": res.Resolved}
			if studentID != nil {
				meta["student_id"] = studentID.String()
			}
			if err := s.Audit.Record(ctx, auditdomain.Event{
				ActorUserID: &actor, Action: "analytics.alerts_recalculated", EntityType: "group", EntityID: groupID.String(), Metadata: meta,
			}); err != nil {
				return err
			}
		}
		res.Open, err = s.Alerts.OpenCounts(ctx, groupID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
