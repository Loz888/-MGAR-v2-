CREATE TABLE workflow_runs (
 run_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), goal text NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','planning','running','verifying','blocked','interrupted','succeeded','failed','cancelled')),
 active_plan_version integer NOT NULL DEFAULT 0 CHECK(active_plan_version>=0),
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0), created_at timestamptz NOT NULL DEFAULT clock_timestamp(), finished_at timestamptz,
 CHECK ((state IN ('succeeded','failed','cancelled')) = (finished_at IS NOT NULL))
);
CREATE TABLE orchestrator_leases (
 run_id uuid PRIMARY KEY REFERENCES workflow_runs, owner_id text NOT NULL,
 epoch bigint NOT NULL CHECK(epoch>=1), lease_until timestamptz NOT NULL,
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0)
);
CREATE TABLE plans (
 run_id uuid NOT NULL REFERENCES workflow_runs, plan_version integer NOT NULL CHECK(plan_version>=1),
 plan_hash text NOT NULL CHECK(plan_hash~'^[0-9a-f]{64}$'), proposer text NOT NULL,
 schema_version text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(run_id,plan_version)
);
CREATE TABLE task_instances (
 task_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES workflow_runs,
 node_key text NOT NULL, role text NOT NULL CHECK(role IN ('research','data','action','verify','aggregate')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ready','claimed','executing','output_ready','verifying','blocked','succeeded','failed','cancelled')),
 owner text, epoch bigint NOT NULL DEFAULT 0 CHECK(epoch>=0), lease_until timestamptz,
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0), retry_count integer NOT NULL DEFAULT 0 CHECK(retry_count>=0),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts>=1),
 result_artifact_id uuid, UNIQUE(run_id,task_id)
);
CREATE TABLE plan_nodes (
 run_id uuid NOT NULL, plan_version integer NOT NULL, node_key text NOT NULL,
 task_instance_id uuid NOT NULL, role text NOT NULL CHECK(role IN ('research','data','action','verify','aggregate')),
 requirements jsonb NOT NULL, reuse_artifact_id uuid,
 PRIMARY KEY(run_id,plan_version,node_key), FOREIGN KEY(run_id,plan_version) REFERENCES plans,
 FOREIGN KEY(run_id,task_instance_id) REFERENCES task_instances(run_id,task_id), UNIQUE(run_id,plan_version,task_instance_id)
);
CREATE TABLE plan_edges (
 run_id uuid NOT NULL, plan_version integer NOT NULL, from_key text NOT NULL, to_key text NOT NULL,
 PRIMARY KEY(run_id,plan_version,from_key,to_key), CHECK(from_key<>to_key),
 FOREIGN KEY(run_id,plan_version,from_key) REFERENCES plan_nodes(run_id,plan_version,node_key),
 FOREIGN KEY(run_id,plan_version,to_key) REFERENCES plan_nodes(run_id,plan_version,node_key)
);
CREATE TABLE artifacts (
 artifact_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES workflow_runs,
 kind text NOT NULL, content_sha256 text NOT NULL CHECK(content_sha256~'^[0-9a-f]{64}$'),
 storage_uri text NOT NULL CHECK(storage_uri LIKE 'artifact://%'), schema_version text NOT NULL,
 producer_task uuid NOT NULL, source_plan_version integer NOT NULL CHECK(source_plan_version>=1),
 byte_length bigint NOT NULL CHECK(byte_length>=0), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 verification_status text NOT NULL DEFAULT 'unverifiable' CHECK(verification_status IN ('passed','failed','unverifiable')),
 UNIQUE(run_id,artifact_id), FOREIGN KEY(run_id,producer_task) REFERENCES task_instances(run_id,task_id),
 FOREIGN KEY(run_id,source_plan_version) REFERENCES plans(run_id,plan_version)
);
ALTER TABLE task_instances ADD FOREIGN KEY(run_id,result_artifact_id) REFERENCES artifacts(run_id,artifact_id);
ALTER TABLE plan_nodes ADD FOREIGN KEY(run_id,reuse_artifact_id) REFERENCES artifacts(run_id,artifact_id);
CREATE TABLE task_inputs (
 task_id uuid NOT NULL, run_id uuid NOT NULL, artifact_id uuid NOT NULL,
 content_sha256 text NOT NULL CHECK(content_sha256~'^[0-9a-f]{64}$'), producer_node text NOT NULL,
 source_plan_version integer NOT NULL, PRIMARY KEY(task_id,artifact_id),
 FOREIGN KEY(run_id,task_id) REFERENCES task_instances(run_id,task_id),
 FOREIGN KEY(run_id,artifact_id) REFERENCES artifacts(run_id,artifact_id),
 FOREIGN KEY(run_id,source_plan_version) REFERENCES plans(run_id,plan_version)
);
CREATE TABLE artifact_verdicts (
 verdict_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), artifact_id uuid NOT NULL REFERENCES artifacts,
 checker_version text NOT NULL, passed boolean NOT NULL, evidence_artifact_id uuid REFERENCES artifacts,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE workflow_events (
 event_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES workflow_runs,
 entity_type text NOT NULL, entity_id uuid NOT NULL, entity_revision bigint NOT NULL CHECK(entity_revision>=0),
 kind text NOT NULL, trace_id uuid NOT NULL DEFAULT gen_random_uuid(), payload jsonb NOT NULL DEFAULT '{}',
 UNIQUE(entity_type,entity_id,entity_revision,kind)
);
CREATE TABLE outbox_events (
 event_id uuid PRIMARY KEY REFERENCES workflow_events, aggregate_type text NOT NULL, aggregate_id uuid NOT NULL,
 event_kind text NOT NULL, payload jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT clock_timestamp(), published_at timestamptz
);
CREATE TABLE inbox_receipts (
 consumer_name text NOT NULL, event_id uuid NOT NULL REFERENCES outbox_events,
 first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(), processing_status text NOT NULL CHECK(processing_status IN ('received','processed')),
 PRIMARY KEY(consumer_name,event_id)
);
CREATE TABLE run_budgets (
 run_id uuid PRIMARY KEY REFERENCES workflow_runs,
 max_model_calls bigint NOT NULL DEFAULT 0 CHECK(max_model_calls>=0), max_tokens_est bigint NOT NULL DEFAULT 0 CHECK(max_tokens_est>=0),
 max_cost_usd numeric(18,6) NOT NULL DEFAULT 0 CHECK(max_cost_usd>=0), max_delegations integer NOT NULL DEFAULT 24 CHECK(max_delegations>=0),
 used_delegations integer NOT NULL DEFAULT 0 CHECK(used_delegations>=0),
 max_concurrency integer NOT NULL DEFAULT 4 CHECK(max_concurrency>=1), running_count integer NOT NULL DEFAULT 0 CHECK(running_count>=0),
 reserved_model_calls bigint NOT NULL DEFAULT 0 CHECK(reserved_model_calls>=0), settled_model_calls bigint NOT NULL DEFAULT 0 CHECK(settled_model_calls>=0),
 reserved_tokens bigint NOT NULL DEFAULT 0 CHECK(reserved_tokens>=0), settled_tokens bigint NOT NULL DEFAULT 0 CHECK(settled_tokens>=0),
 reserved_cost numeric(18,6) NOT NULL DEFAULT 0 CHECK(reserved_cost>=0), settled_cost numeric(18,6) NOT NULL DEFAULT 0 CHECK(settled_cost>=0),
 CHECK(used_delegations<=max_delegations), CHECK(running_count<=max_concurrency),
 CHECK(reserved_model_calls+settled_model_calls<=max_model_calls), CHECK(reserved_tokens+settled_tokens<=max_tokens_est), CHECK(reserved_cost+settled_cost<=max_cost_usd)
);
CREATE TABLE model_calls (
 call_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), logical_call_id uuid NOT NULL, run_id uuid NOT NULL REFERENCES workflow_runs,
 task_id uuid NOT NULL, provider text NOT NULL, model text NOT NULL,
 status text NOT NULL CHECK(status IN ('reserved','succeeded','failed','unknown')), usage jsonb NOT NULL DEFAULT '{}',
 estimate_bound numeric(18,6) NOT NULL CHECK(estimate_bound>=0), reservation jsonb NOT NULL, settlement jsonb,
 usage_known boolean NOT NULL DEFAULT false, usage_source text NOT NULL DEFAULT 'unknown', created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(run_id,task_id) REFERENCES task_instances(run_id,task_id)
);
CREATE TABLE tool_invocations (
 invocation_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), task_id uuid NOT NULL REFERENCES task_instances,
 capability text NOT NULL, scope jsonb NOT NULL, request_hash text NOT NULL CHECK(request_hash~'^[0-9a-f]{64}$'),
 effect_class text NOT NULL CHECK(effect_class IN ('readonly','idempotent_write','non_idempotent')),
 status text NOT NULL CHECK(status IN ('reserved','succeeded','failed','unknown')), result_artifact_id uuid REFERENCES artifacts
);
CREATE TABLE approval_requests (
 approval_id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES workflow_runs,
 task_id uuid NOT NULL, action_digest text NOT NULL CHECK(action_digest~'^[0-9a-f]{64}$'), requested_scope jsonb NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','approved','denied','expired','consumed')), approver text, expiry timestamptz NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES task_instances(run_id,task_id)
);
CREATE INDEX task_ready_idx ON task_instances(run_id,state,task_id);
CREATE INDEX edges_to_idx ON plan_edges(run_id,plan_version,to_key);
CREATE INDEX outbox_unpublished_idx ON outbox_events(created_at) WHERE published_at IS NULL;
CREATE INDEX events_run_idx ON workflow_events(run_id,entity_revision);
CREATE INDEX model_task_idx ON model_calls(task_id,status);
CREATE FUNCTION reject_history_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable history' USING ERRCODE='23514'; END $$;
CREATE FUNCTION preserve_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state IN ('succeeded','failed','cancelled') AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'terminal is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER workflow_terminal BEFORE UPDATE ON workflow_runs FOR EACH ROW EXECUTE FUNCTION preserve_terminal();
CREATE TRIGGER task_terminal BEFORE UPDATE ON task_instances FOR EACH ROW EXECUTE FUNCTION preserve_terminal();
CREATE TRIGGER plans_immutable BEFORE UPDATE OR DELETE ON plans FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER nodes_immutable BEFORE UPDATE OR DELETE ON plan_nodes FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER edges_immutable BEFORE UPDATE OR DELETE ON plan_edges FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER artifacts_immutable BEFORE UPDATE OR DELETE ON artifacts FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER verdicts_immutable BEFORE UPDATE OR DELETE ON artifact_verdicts FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER inputs_immutable BEFORE UPDATE OR DELETE ON task_inputs FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER events_immutable BEFORE UPDATE OR DELETE ON workflow_events FOR EACH ROW EXECUTE FUNCTION reject_history_change();
DO $$ BEGIN CREATE ROLE mgar_reader NOLOGIN; EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE mgar_worker NOLOGIN; EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$;
DO $$ BEGIN
 EXECUTE format('GRANT USAGE ON SCHEMA %I TO mgar_reader,mgar_worker',current_schema());
 EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO mgar_reader',current_schema());
END $$;
