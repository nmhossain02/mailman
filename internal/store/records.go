package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/nabeel/mailman/internal/core"
)

func marshal(v any) ([]byte, error) { return json.Marshal(v) }

func (s *DB) SaveRule(ctx context.Context, r core.Rule) error {
	conditions, err := marshal(r.Conditions)
	if err != nil {
		return err
	}
	exceptions, err := marshal(r.Exceptions)
	if err != nil {
		return err
	}
	actions, err := marshal(r.Actions)
	if err != nil {
		return err
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO rules(id,account_id,source,provider_id,name,enabled,read_only,sequence,conditions_json,exceptions_json,actions_json,raw_provider,canonical_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,source=excluded.source,provider_id=excluded.provider_id,name=excluded.name,enabled=excluded.enabled,read_only=excluded.read_only,sequence=excluded.sequence,conditions_json=excluded.conditions_json,exceptions_json=excluded.exceptions_json,actions_json=excluded.actions_json,raw_provider=excluded.raw_provider,canonical_hash=excluded.canonical_hash`, r.ID, r.AccountID, r.Source, r.ProviderID, r.Name, r.Enabled, r.ReadOnly, r.Sequence, conditions, exceptions, actions, r.RawProvider, r.CanonicalHash)
	return err
}

func (s *DB) Rules(ctx context.Context) ([]core.Rule, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,account_id,source,provider_id,name,enabled,read_only,sequence,conditions_json,exceptions_json,actions_json,raw_provider,canonical_hash FROM rules ORDER BY account_id,name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Rule
	for rows.Next() {
		var r core.Rule
		var sequence sql.NullInt64
		var conditions, exceptions, actions []byte
		if err = rows.Scan(&r.ID, &r.AccountID, &r.Source, &r.ProviderID, &r.Name, &r.Enabled, &r.ReadOnly, &sequence, &conditions, &exceptions, &actions, &r.RawProvider, &r.CanonicalHash); err != nil {
			return nil, err
		}
		if sequence.Valid {
			v := int(sequence.Int64)
			r.Sequence = &v
		}
		if err = json.Unmarshal(conditions, &r.Conditions); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(exceptions, &r.Exceptions); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(actions, &r.Actions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *DB) DeleteRule(ctx context.Context, id string) error {
	_, err := s.sql.ExecContext(ctx, `DELETE FROM rules WHERE id=?`, id)
	return err
}

func (s *DB) SavePlan(ctx context.Context, p core.Plan) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO plans(id,name,status,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,status=excluded.status`, p.ID, p.Name, p.Status, p.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM operations WHERE plan_id=?`, p.ID); err != nil {
		return err
	}
	for _, op := range p.Operations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,plan_id,execution_key,target_type,target_id,kind,risk,arguments_json,expected_revision,status) VALUES(?,?,?,?,?,?,?,?,?,?)`, op.ID, p.ID, op.ExecutionKey, op.TargetType, op.TargetID, op.Kind, op.Risk, op.Arguments, op.ExpectedRevision, op.Status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *DB) Plans(ctx context.Context) ([]core.Plan, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,name,status,created_at FROM plans ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Plan
	for rows.Next() {
		var p core.Plan
		var created string
		if err = rows.Scan(&p.ID, &p.Name, &p.Status, &created); err != nil {
			return nil, err
		}
		p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Operations, err = s.planOperations(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *DB) planOperations(ctx context.Context, planID string) ([]core.Operation, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,execution_key,target_type,target_id,kind,risk,arguments_json,expected_revision,status FROM operations WHERE plan_id=? ORDER BY rowid`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Operation
	for rows.Next() {
		var op core.Operation
		if err = rows.Scan(&op.ID, &op.ExecutionKey, &op.TargetType, &op.TargetID, &op.Kind, &op.Risk, &op.Arguments, &op.ExpectedRevision, &op.Status); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *DB) SaveSchedule(ctx context.Context, v core.Schedule) error {
	accounts, err := marshal(v.AccountIDs)
	if err != nil {
		return err
	}
	rules, err := marshal(v.RuleIDs)
	if err != nil {
		return err
	}
	route, err := marshal(v.Route)
	if err != nil {
		return err
	}
	var last any
	if v.LastRunAt != nil {
		last = v.LastRunAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO schedules(id,name,draft_plan_name,enabled,every_seconds,account_ids_json,rule_ids_json,route_json,last_run_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,draft_plan_name=excluded.draft_plan_name,enabled=excluded.enabled,every_seconds=excluded.every_seconds,account_ids_json=excluded.account_ids_json,rule_ids_json=excluded.rule_ids_json,route_json=excluded.route_json,last_run_at=excluded.last_run_at`, v.ID, v.Name, v.DraftPlanName, v.Enabled, v.EverySeconds, accounts, rules, route, last)
	return err
}
func (s *DB) Schedules(ctx context.Context) ([]core.Schedule, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,name,draft_plan_name,enabled,every_seconds,account_ids_json,rule_ids_json,route_json,last_run_at FROM schedules ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Schedule
	for rows.Next() {
		var v core.Schedule
		var accounts, rules, route []byte
		var last sql.NullString
		if err = rows.Scan(&v.ID, &v.Name, &v.DraftPlanName, &v.Enabled, &v.EverySeconds, &accounts, &rules, &route, &last); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(accounts, &v.AccountIDs); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(rules, &v.RuleIDs); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(route, &v.Route); err != nil {
			return nil, err
		}
		if last.Valid {
			parsed, e := time.Parse(time.RFC3339Nano, last.String)
			if e != nil {
				return nil, e
			}
			v.LastRunAt = &parsed
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *DB) SaveEvalLabel(ctx context.Context, v core.EvalLabel) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO eval_labels(case_id,trace_id,source,expected_json,created_at) VALUES(?,?,?,?,?) ON CONFLICT(case_id,trace_id,source) DO UPDATE SET expected_json=excluded.expected_json,created_at=excluded.created_at`, v.CaseID, v.TraceID, v.Source, v.ExpectedJSON, v.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *DB) SaveEvalRun(ctx context.Context, v core.EvalRunConfig, status string, now time.Time) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO eval_runs(id,config_json,status,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET config_json=excluded.config_json,status=excluded.status`, v.ID, b, status, now.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *DB) SaveGrant(ctx context.Context, v core.IntegrationGrant) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO integration_grants(id,grant_json) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET grant_json=excluded.grant_json`, v.ID, b)
	return err
}
