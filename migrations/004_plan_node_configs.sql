CREATE TABLE plan_node_configs (
 run_id uuid NOT NULL, plan_version integer NOT NULL, node_key text NOT NULL,
 model_config_version text NOT NULL CHECK(model_config_version~'^[A-Za-z0-9._:-]{1,128}$'),
 tool_policy_version text NOT NULL CHECK(tool_policy_version~'^[A-Za-z0-9._:-]{1,128}$'),
 output_tool_version text NOT NULL CHECK(output_tool_version~'^[A-Za-z0-9._:-]{1,128}$'),
 PRIMARY KEY(run_id,plan_version,node_key),
 FOREIGN KEY(run_id,plan_version,node_key) REFERENCES plan_nodes(run_id,plan_version,node_key)
);
CREATE TRIGGER plan_configs_immutable BEFORE UPDATE OR DELETE ON plan_node_configs FOR EACH ROW EXECUTE FUNCTION reject_history_change();
GRANT SELECT ON plan_node_configs TO mgar_reader;
