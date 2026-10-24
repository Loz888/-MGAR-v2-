CREATE TABLE resource_pools (
 class text PRIMARY KEY CHECK(class IN ('llm','cpu','db')),
 capacity integer NOT NULL CHECK(capacity BETWEEN 1 AND 64),
 queue_limit integer NOT NULL CHECK(queue_limit BETWEEN 1 AND 1024)
);
INSERT INTO resource_pools VALUES('llm',2,64),('cpu',2,64),('db',4,64);
CREATE TABLE resource_requests (
 request_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,task_id uuid NOT NULL,owner text NOT NULL,epoch bigint NOT NULL CHECK(epoch>=1),plan_version integer NOT NULL CHECK(plan_version>=1),
 class text NOT NULL REFERENCES resource_pools,
 token_hash text NOT NULL CHECK(token_hash~'^[0-9a-f]{64}$'),
 priority integer NOT NULL CHECK(priority BETWEEN 0 AND 10),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),deadline timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('queued','held','released','cancelled','expired','unknown')),
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 FOREIGN KEY(run_id,task_id) REFERENCES task_instances(run_id,task_id),
 CHECK(deadline>created_at)
);
CREATE INDEX resource_queue_idx ON resource_requests(class,state,created_at,request_id);
CREATE INDEX resource_task_idx ON resource_requests(run_id,task_id);
GRANT SELECT ON resource_pools,resource_requests TO mgar_reader;
