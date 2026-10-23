"""Policy plus actual database ACL tests: neither regex nor prompt is authority."""
from dataclasses import replace

import psycopg
import pytest

from mgar_agents.data import ReaderCredentials, ReaderLimits, SQLPolicy, SQLReader


@pytest.mark.parametrize('suffix', [
    '; DELETE FROM x', ' -- bypass\n', ' /* bypass */', ' FOR UPDATE',
    ' UNION SELECT 1', ' CROSS JOIN pg_catalog.pg_authid',
])
def test_sql_policy_rejects_injection_and_multi_statement(suffix):
    policy = SQLPolicy('mgar_fixture_0123456789abcdef')
    with pytest.raises(PermissionError):
        policy.validate('SELECT order_id FROM mgar_fixture_0123456789abcdef.orders' + suffix, [])


@pytest.mark.parametrize('statement', [
    'DELETE FROM {s}.orders', 'UPDATE {s}.orders SET amount=0', 'CREATE TABLE x(n int)',
    'SELECT order_id INTO x FROM {s}.orders', 'WITH x AS (DELETE FROM {s}.orders RETURNING *) SELECT * FROM x',
    "SELECT pg_read_file('/etc/passwd') FROM {s}.orders", 'SELECT pg_sleep(10) FROM {s}.orders',
    'SELECT * FROM pg_catalog.pg_authid', 'SELECT * FROM public.workflow_runs',
    'SELECT * FROM another_schema.orders', 'SELECT current_setting(\'data_directory\') FROM {s}.orders',
    'SELECT order_id FROM {s}.orders LIMIT 999999999',
])
def test_sql_policy_ast_rejects_dangerous_shapes(statement):
    policy = SQLPolicy('mgar_fixture_0123456789abcdef')
    with pytest.raises(PermissionError):
        policy.validate(statement.format(s=policy.schema), [])


def test_sql_parameter_binding_does_not_interpret_injection_text(data_fixture):
    value = "paid'; DELETE FROM orders; --"
    result = data_fixture.reader.query(f'SELECT order_id FROM {data_fixture.schema}.orders WHERE status=%s', [value], timeout_s=1)
    assert result['rows'] == [] and result['parameters'] == [value]
    assert data_fixture.reader.query(f'SELECT COUNT(*) AS n FROM {data_fixture.schema}.orders', [], timeout_s=1)['rows'] == [[4]]


def test_reader_credentials_are_independent_acl_even_with_readonly_disabled(data_fixture):
    with data_fixture.connect_reader(readonly=False) as reader:
        assert reader.execute('SHOW default_transaction_read_only').fetchone()[0] == 'off'
        for statement in [f'UPDATE {data_fixture.schema}.orders SET amount=0',
                          f'CREATE TABLE {data_fixture.schema}.forbidden(n int)',
                          'SELECT * FROM public.workflow_runs']:
            with pytest.raises(psycopg.errors.InsufficientPrivilege):
                reader.execute(statement)
        assert reader.execute(f'SELECT COUNT(*) FROM {data_fixture.schema}.orders').fetchone()[0] == 4
        flags = reader.execute('SELECT rolsuper,rolcreatedb,rolcreaterole,rolbypassrls FROM pg_roles WHERE rolname=current_user').fetchone()
        assert flags == (False, False, False, False)
        assert reader.execute("SELECT pg_has_role(current_user, 'mgar_reader','member')").fetchone()[0] is False


def test_readonly_transaction_is_a_separate_database_guard(data_fixture):
    with data_fixture.connect_reader(readonly=False) as reader:
        reader.execute('BEGIN READ ONLY')
        try:
            with pytest.raises(psycopg.errors.ReadOnlySqlTransaction):
                reader.execute(f'UPDATE {data_fixture.schema}.orders SET amount=0')
        finally:
            reader.execute('ROLLBACK')


@pytest.mark.parametrize('dsn', [
    'postgres://reader:stub@127.0.0.1:5432/mgar_test',
    'postgres://reader:stub@example.com:55432/mgar_test',
    'postgres://reader:stub@127.0.0.1:55432/production',
    'postgres://reader:stub@127.0.0.1:55432/mgar_test?host=example.com',
    'postgres://reader:stub@127.0.0.1:55432/mgar_test?hostaddr=192.0.2.1',
    'postgres://reader:stub@127.0.0.1:55432/mgar_test?options=-csearch_path=public',
    'postgres://reader:stub@127.0.0.1:55432/mgar_test?service=production',
])
def test_reader_rejects_routing_overrides_and_nonsynthetic_targets(dsn):
    with pytest.raises((ValueError, PermissionError)):
        ReaderCredentials.from_dsn(dsn)


def test_reader_admin_credentials_are_refused_before_user_query(data_fixture):
    import conftest
    from urllib.parse import quote
    k = conftest._admin_kwargs()
    dsn = f"postgres://{quote(k['user'])}:{quote(k['password'])}@127.0.0.1:55432/{k['dbname']}?sslmode=disable"
    credentials = ReaderCredentials.from_dsn(dsn)
    reader = SQLReader(credentials, data_fixture.policy, 'incident-orders.v1', data_fixture.reader.fixture_sha256, ReaderLimits())
    with pytest.raises(PermissionError):
        reader.query(f'SELECT order_id FROM {data_fixture.schema}.orders', [], timeout_s=1)


def test_reader_enforces_its_own_limit_validation(data_fixture):
    for change in [{'max_rows': True}, {'max_rows': 0}, {'max_output_bytes': False}, {'statement_timeout_ms': 0}, {'connect_timeout_s': float('nan')}]:
        with pytest.raises(ValueError):
            replace(ReaderLimits(), **change)


def test_reader_refuses_non_superuser_credentials_that_have_writes(data_fixture):
    import conftest
    from psycopg import sql
    with psycopg.connect(**conftest._admin_kwargs(), autocommit=True) as admin:
        admin.execute(sql.SQL('GRANT UPDATE ON {}.orders TO {}').format(sql.Identifier(data_fixture.schema), sql.Identifier(data_fixture.role)))
        try:
            with data_fixture.connect_reader(readonly=False) as reader:
                assert reader.execute("SELECT has_table_privilege(current_user,%s,'UPDATE')", [data_fixture.schema+'.orders']).fetchone()[0] is True
            with pytest.raises(PermissionError):
                data_fixture.reader.query(f'SELECT COUNT(*) AS n FROM {data_fixture.schema}.orders', [], timeout_s=1)
        finally:
            admin.execute(sql.SQL('REVOKE UPDATE ON {}.orders FROM {}').format(sql.Identifier(data_fixture.schema), sql.Identifier(data_fixture.role)))
