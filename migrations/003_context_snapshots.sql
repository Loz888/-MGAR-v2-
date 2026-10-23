CREATE TABLE context_snapshots (
 snapshot_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL, task_id uuid NOT NULL UNIQUE,
 plan_version integer NOT NULL CHECK(plan_version>=1),
 content_sha256 text NOT NULL CHECK(content_sha256~'^[0-9a-f]{64}$'),
 body text NOT NULL CHECK(jsonb_typeof(body::jsonb)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(run_id,snapshot_id),
 FOREIGN KEY(run_id,task_id) REFERENCES task_instances(run_id,task_id),
 FOREIGN KEY(run_id,plan_version) REFERENCES plans(run_id,plan_version)
);
CREATE TABLE artifact_contexts (
 artifact_id uuid PRIMARY KEY, run_id uuid NOT NULL, snapshot_id uuid NOT NULL,
 FOREIGN KEY(run_id,artifact_id) REFERENCES artifacts(run_id,artifact_id),
 FOREIGN KEY(run_id,snapshot_id) REFERENCES context_snapshots(run_id,snapshot_id)
);
CREATE TRIGGER snapshots_immutable BEFORE UPDATE OR DELETE ON context_snapshots FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE TRIGGER artifact_contexts_immutable BEFORE UPDATE OR DELETE ON artifact_contexts FOR EACH ROW EXECUTE FUNCTION reject_history_change();
GRANT SELECT ON context_snapshots,artifact_contexts TO mgar_reader;
