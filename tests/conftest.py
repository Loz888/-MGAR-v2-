"""Owned synthetic PostgreSQL schemas and independent reader logins for P2 tests."""
from dataclasses import dataclass, field
from contextlib import contextmanager
import hashlib
from pathlib import Path
import re
import secrets
from urllib.parse import parse_qs, quote, unquote, urlsplit

import psycopg
from psycopg import sql
import pytest

from mgar_agents.data import ReaderCredentials, ReaderLimits, SQLPolicy, SQLReader

ROOT = Path(__file__).resolve().parents[1]


def _admin_kwargs():
    import os
    value = os.environ.get('MGAR_TEST_DATABASE_URL', '')
    if not value:
        p = ROOT/'.local/db.env'
        if p.exists():
            values = dict(line.split('=', 1) for line in p.read_text().splitlines())
            value = values.get('MGAR_TEST_DATABASE_URL', '')
    try:
        u = urlsplit(value)
        params = parse_qs(u.query, keep_blank_values=True)
        if (u.scheme not in {'postgres', 'postgresql'} or u.hostname not in {'127.0.0.1', 'localhost'}
                or u.port != 55432 or not re.fullmatch(r'mgar_test(?:_[0-9a-f]{16})?', u.path[1:])
                or set(params) - {'sslmode', 'connect_timeout'} or any(len(v) != 1 for v in params.values())
                or not u.username or not u.password):
            raise ValueError('invalid synthetic target')
        return {'host': '127.0.0.1', 'hostaddr': '127.0.0.1', 'port': 55432, 'dbname': u.path[1:],
                'user': unquote(u.username), 'password': unquote(u.password), 'sslmode': 'disable',
                'connect_timeout': 3, 'options': '-c default_transaction_read_only=off'}
    except Exception:
        raise RuntimeError('Synthetic PostgreSQL fixture requires the guarded local test target') from None


@dataclass(repr=False)
class DataFixture:
    schema: str
    role: str
    credentials: ReaderCredentials
    policy: SQLPolicy
    reader: SQLReader
    _kwargs: dict = field(repr=False)

    def connect_reader(self, readonly=True):
        kwargs = dict(self._kwargs)
        kwargs['options'] = '-c default_transaction_read_only=' + ('on' if readonly else 'off')
        return psycopg.connect(**kwargs, autocommit=True)

    @contextmanager
    def lock_orders(self):
        with psycopg.connect(**_admin_kwargs()) as admin:
            with admin.cursor() as cur:
                cur.execute(sql.SQL('LOCK TABLE {}.orders IN ACCESS EXCLUSIVE MODE').format(sql.Identifier(self.schema)))
                yield
            admin.rollback()


@pytest.fixture
def data_fixture():
    kwargs = _admin_kwargs()
    schema = 'mgar_fixture_' + secrets.token_hex(8)
    role = 'mgar_data_reader_' + secrets.token_hex(8)
    password = secrets.token_hex(24)
    seed = (ROOT/'fixtures/incident/sql/orders.sql').read_bytes()
    admin = psycopg.connect(**kwargs, autocommit=True)
    created_schema = created_role = False
    try:
        admin.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(schema)))
        created_schema = True
        admin.execute(sql.SQL('CREATE ROLE {} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD {}').format(sql.Identifier(role), sql.Literal(password)))
        created_role = True
        admin.execute(sql.SQL('ALTER ROLE {} SET default_transaction_read_only=on').format(sql.Identifier(role)))
        admin.execute(sql.SQL('ALTER ROLE {} SET search_path=pg_catalog').format(sql.Identifier(role)))
        seed_sql = seed.decode().replace('__SCHEMA__', sql.Identifier(schema).as_string(admin))
        admin.execute(seed_sql)
        admin.execute(sql.SQL('GRANT USAGE ON SCHEMA {} TO {}').format(sql.Identifier(schema), sql.Identifier(role)))
        admin.execute(sql.SQL('GRANT SELECT ON {}.orders TO {}').format(sql.Identifier(schema), sql.Identifier(role)))
        reader_kwargs = dict(kwargs, user=role, password=password)
        reader_kwargs['options'] = '-c default_transaction_read_only=on'
        dsn = f"postgres://{quote(role)}:{quote(password)}@127.0.0.1:55432/{kwargs['dbname']}?sslmode=disable"
        credentials = ReaderCredentials.from_dsn(dsn)
        policy = SQLPolicy(schema)
        reader = SQLReader(credentials, policy, 'incident-orders.v1', hashlib.sha256(seed).hexdigest(), ReaderLimits())
        yield DataFixture(schema, role, credentials, policy, reader, reader_kwargs)
    finally:
        if created_schema:
            admin.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(schema)))
        if created_role:
            admin.execute(sql.SQL('DROP ROLE {}').format(sql.Identifier(role)))
        admin.close()
