-- Add the P6 tool counters to the existing single workflow budget row.
ALTER TABLE run_budgets ADD COLUMN max_tool_calls bigint NOT NULL DEFAULT 64 CHECK(max_tool_calls>=0),
 ADD COLUMN reserved_tool_calls bigint NOT NULL DEFAULT 0 CHECK(reserved_tool_calls>=0),
 ADD COLUMN settled_tool_calls bigint NOT NULL DEFAULT 0 CHECK(settled_tool_calls>=0),
 ADD COLUMN max_call_concurrency bigint NOT NULL DEFAULT 4 CHECK(max_call_concurrency>=1);
UPDATE run_budgets b SET
 reserved_tool_calls=(SELECT count(*) FROM tool_invocations v JOIN task_instances t USING(task_id) WHERE t.run_id=b.run_id AND v.status IN ('reserved','unknown')),
 settled_tool_calls=(SELECT count(*) FROM tool_invocations v JOIN task_instances t USING(task_id) WHERE t.run_id=b.run_id AND v.status IN ('succeeded','failed'));
UPDATE run_budgets SET max_tool_calls=GREATEST(max_tool_calls,reserved_tool_calls+settled_tool_calls),
 max_call_concurrency=GREATEST(max_call_concurrency,reserved_model_calls+reserved_tool_calls);
ALTER TABLE run_budgets ADD CHECK(reserved_tool_calls+settled_tool_calls<=max_tool_calls),
 ADD CHECK(reserved_model_calls+reserved_tool_calls<=max_call_concurrency);
