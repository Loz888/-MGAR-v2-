"""Independent Data admission, database privilege and numeric-source boundaries."""
from dataclasses import replace
import json
import re
from uuid import uuid4

import psycopg
from psycopg.conninfo import conninfo_to_dict
import pytest

from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task


def inputs():
    claim = Claim(str(uuid4()), str(uuid4()), "data-boundary-worker", "data", 1, 0, 1)
    task = Task(claim, "Count the synthetic order rows", "data-boundaries")
    snapshot = ContextSnapshot.from_dict({"schema_version": "context.v1", "task_id": claim.task_id,
                                         "plan_version": 1, "model_config_version": "mock.v1",
                                         "tool_policy_version": "data-boundaries.v1", "artifacts": []})
    return task, snapshot


def tool_query(sql, parameters=None):
    return json.dumps({"type": "tool_call", "call_id": "count_orders", "name": "data.query",
                       "arguments": {"sql": sql, "parameters": parameters or []}})


def data_run(fixture, choose, *, task=None, snapshot=None):
    from mgar_agents.data import DataAgent
    if task is None:
        task, snapshot = inputs()
    return DataAgent(MockProvider(choose), fixture.reader).run(task, snapshot, Budget())


def metric_output(observation, *, value=None):
    value = observation.output["rows"][0][0] if value is None else value
    return {"summary": "Observed synthetic order row count.", "metrics": [{"call_id": observation.call_id,
            "row": 0, "column": "counted", "value": value}]}


@pytest.mark.parametrize("target", [
    "postgres://reader:dummy@127.0.0.1:5432/mgar_test",
    "postgres://reader:dummy@127.0.0.1:55432/production",
    "postgres://reader:dummy@127.0.0.1:55432/mgar_test?hostaddr=203.0.113.1",
    "postgres://reader:dummy@127.0.0.1:55432/mgar_test?host=203.0.113.1",
    "postgres://reader:dummy@127.0.0.1:55432/mgar_test?service=unexpected",
    "postgres://reader:dummy@127.0.0.1:55432/mgar_test?options=-cdefault_transaction_read_only=off",
    "postgres://reader:dummy@127.0.0.1:55432/mgar_test?sslmode=disable&sslmode=require",
    "host=127.0.0.1 port=55432 dbname=mgar_test user=reader password=dummy",
])
def test_untrusted_dsn_cannot_change_the_exact_synthetic_target(target):
    from mgar_agents.data import ReaderCredentials
    with pytest.raises((ValueError, PermissionError)):
        ReaderCredentials.from_dsn(target)


def test_environment_cannot_override_hostaddr_or_readonly_options(data_fixture, monkeypatch):
    original_connect = psycopg.connect
    seen = []

    def guarded_connect(conninfo="", **kwargs):
        connection_fields = conninfo_to_dict(conninfo, **{k: v for k, v in kwargs.items() if k not in {"autocommit", "row_factory", "context", "prepare_threshold", "cursor_factory"}})
        # Assert before the actual connection: no attempted out-of-scope routing.
        assert connection_fields.get("host") == "127.0.0.1"
        assert connection_fields.get("hostaddr") == "127.0.0.1"
        assert str(connection_fields.get("port")) == "55432"
        assert re.fullmatch(r"mgar_test(?:_[0-9a-f]{16})?", connection_fields.get("dbname", ""))
        assert "default_transaction_read_only=on" in connection_fields.get("options", "")
        seen.append(True)
        return original_connect(conninfo, **kwargs)

    monkeypatch.setenv("PGHOSTADDR", "203.0.113.1")
    monkeypatch.setenv("PGOPTIONS", "-c default_transaction_read_only=off -c search_path=public")
    monkeypatch.setattr(psycopg, "connect", guarded_connect)
    observed = data_fixture.reader.query(f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders", [], timeout_s=1)
    assert seen and observed["rows"][0][0] == 4
    assert observed["verified"] is False


def test_database_acl_blocks_writes_even_without_readonly_guc(data_fixture):
    # This explicit test-infrastructure escape bypasses application AST/READ ONLY
    # to establish that the separate reader credential still lacks table writes.
    with data_fixture.connect_reader(readonly=False) as connection:
        for statement in [f"UPDATE {data_fixture.schema}.orders SET amount=0",
                          f"CREATE TABLE {data_fixture.schema}.unauthorized(value integer)",
                          "SELECT * FROM public.workflow_runs"]:
            with pytest.raises(psycopg.errors.InsufficientPrivilege):
                connection.execute(statement)
    observed = data_fixture.reader.query(f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders", [], timeout_s=1)
    assert observed["rows"][0][0] == 4


def test_actual_query_and_metrics_have_a_positive_control(data_fixture):
    statement = f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders"

    def choose(request):
        if request.turn == 1:
            return tool_query(statement)
        return json.dumps({"type": "final", "output": metric_output(request.observations[-1])})

    result = data_run(data_fixture, choose)
    assert result.status == "completed" and result.verified is False
    assert result.observations[0].output["rows"][0][0] == 4
    assert result.observations[0].output["row_count"] == 1
    assert result.observations[0].output["snapshot_id"]
    assert result.observations[0].output["fixture_version"]


@pytest.mark.parametrize("mutation", ["wrong_value", "numeric_type", "wrong_call", "wrong_column", "wrong_row", "verified"])
def test_model_metrics_must_match_the_exact_observed_cell(data_fixture, mutation):
    statement = f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders"

    def choose(request):
        if request.turn == 1:
            return tool_query(statement)
        output = metric_output(request.observations[-1])
        metric = output["metrics"][0]
        if mutation == "wrong_value":
            metric["value"] = 987654
        elif mutation == "numeric_type":
            metric["value"] = float(metric["value"])
        elif mutation == "wrong_call":
            metric["call_id"] = "never-observed"
        elif mutation == "wrong_column":
            metric["column"] = "never_observed"
        elif mutation == "wrong_row":
            metric["row"] = 99
        else:
            output["verified"] = True
        return json.dumps({"type": "final", "output": output})

    result = data_run(data_fixture, choose)
    assert result.status == "failed" and result.verified is False
    assert result.output is None


def test_a_new_task_cannot_borrow_earlier_reader_observations(data_fixture):
    statement = f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders"

    def choose(request):
        if request.turn == 1:
            return tool_query(statement)
        return json.dumps({"type": "final", "output": metric_output(request.observations[-1])})

    first = data_run(data_fixture, choose)
    assert first.status == "completed"
    borrowed = metric_output(first.observations[0])
    second = data_run(data_fixture, [json.dumps({"type": "final", "output": borrowed})])
    assert second.status == "failed" and second.observations == ()


def test_non_data_role_does_not_invoke_the_provider(data_fixture):
    from mgar_agents.data import DataAgent
    task, snapshot = inputs()
    task = replace(task, claim=replace(task.claim, role="research"))
    provider = MockProvider([json.dumps({"type": "final", "output": {"summary": "fake", "metrics": []}})])
    result = DataAgent(provider, data_fixture.reader).run(task, snapshot, Budget())
    assert result.status == "failed" and result.error_kind == "policy_denied"
    assert provider.requests == []


@pytest.mark.parametrize("suffix,parameters", [
    (" WHERE", []),
    (" WHERE amount =", []),
    (" WHERE (status = %s) trailing_garbage", ["paid"]),
])
def test_sql_parser_errors_are_not_normalized_into_an_authorized_query(suffix, parameters):
    from mgar_agents.data import SQLPolicy
    policy = SQLPolicy("mgar_fixture_0123456789abcdef")
    statement = "SELECT order_id FROM mgar_fixture_0123456789abcdef.orders" + suffix
    with pytest.raises(PermissionError):
        policy.validate(statement, parameters)


def test_expired_parse_budget_does_not_start_a_database_connection(data_fixture, monkeypatch):
    import time
    from mgar_agents.data import SQLPolicy

    class SlowPolicy(SQLPolicy):
        def validate(self, statement, parameters):
            normalized = super().validate(statement, parameters)
            time.sleep(.03)
            return normalized

    reader = replace(data_fixture.reader, policy=SlowPolicy(data_fixture.schema))
    original_connect = psycopg.connect
    connections = []

    def observed_connect(*args, **kwargs):
        connections.append(True)
        return original_connect(*args, **kwargs)

    monkeypatch.setattr(psycopg, "connect", observed_connect)
    with pytest.raises(TimeoutError):
        reader.query(f"SELECT COUNT(*) AS counted FROM {data_fixture.schema}.orders", [], timeout_s=.01)
    assert connections == [], "expired parsing budget still opened a PostgreSQL connection"
