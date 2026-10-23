"""Independent-reader PostgreSQL adapter for synthetic numeric evidence."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date, datetime
from decimal import Decimal
import hashlib
import math
import re
import time
from typing import Any, cast
from urllib.parse import parse_qs, unquote, urlsplit

import psycopg
from sqlglot import parse_one, exp

from mgar_agents.core.schemas import MAX_JSON_BYTES, canonical_json
from mgar_agents.core.tools import PolicyDenied, ToolRegistry, ToolSpec

from .policy import SQLPolicy


@dataclass(frozen=True, repr=False)
class ReaderCredentials:
    database: str
    username: str
    password: str = field(repr=False)

    def __post_init__(self) -> None:
        if not re.fullmatch(r'mgar_test(?:_[0-9a-f]{16})?', self.database) or not self.username or not self.password or len(self.username) > 128 or len(self.password) > 1024:
            raise ValueError('invalid synthetic reader credentials')

    @classmethod
    def from_dsn(cls, value: str) -> ReaderCredentials:
        try:
            u = urlsplit(value)
            query = parse_qs(u.query, keep_blank_values=True)
            if (u.scheme not in {'postgres', 'postgresql'} or u.hostname not in {'127.0.0.1', 'localhost'} or u.port != 55432
                    or u.fragment or not u.username or not u.password or set(query) - {'sslmode', 'connect_timeout'}
                    or any(len(v) != 1 for v in query.values()) or query.get('sslmode', ['disable']) != ['disable']):
                raise ValueError('invalid target')
            if 'connect_timeout' in query and not 1 <= int(query['connect_timeout'][0]) <= 5:
                raise ValueError('invalid connection timeout')
            return cls(u.path[1:], unquote(u.username), unquote(u.password))
        except Exception:
            raise ValueError('reader connection must use the guarded local synthetic target') from None

    def connection_kwargs(self, timeout_s: int) -> dict[str, Any]:
        # Explicit hostaddr/options override libpq environment routing/GUCs.
        return {'host': '127.0.0.1', 'hostaddr': '127.0.0.1', 'port': 55432, 'dbname': self.database,
                'user': self.username, 'password': self.password, 'sslmode': 'disable',
                'connect_timeout': timeout_s, 'options': '-c default_transaction_read_only=on -c search_path=pg_catalog'}


@dataclass(frozen=True)
class ReaderLimits:
    max_rows: int = 100
    max_output_bytes: int = 16384
    statement_timeout_ms: int = 500
    lock_timeout_ms: int = 200
    connect_timeout_s: int = 3

    def __post_init__(self) -> None:
        limits = ((self.max_rows, 1, 100), (self.max_output_bytes, 1, MAX_JSON_BYTES),
                  (self.statement_timeout_ms, 1, 5000), (self.lock_timeout_ms, 1, 5000), (self.connect_timeout_s, 1, 5))
        if any(type(value) is not int or not low <= value <= high for value, low, high in limits):
            raise ValueError('reader resource limits must be finite bounded integers')


def _cell(value: Any) -> Any:
    if value is None or type(value) in {str, int, bool}:
        return value
    if type(value) is float and math.isfinite(value):
        return value
    if isinstance(value, Decimal) and value.is_finite():
        return str(value)
    if isinstance(value, (date, datetime)):
        return value.isoformat()
    raise PolicyDenied('unsupported SQL result type')


@dataclass(frozen=True, repr=False)
class SQLReader:
    credentials: ReaderCredentials
    policy: SQLPolicy
    fixture_version: str
    fixture_sha256: str
    limits: ReaderLimits = field(default_factory=ReaderLimits)

    def __post_init__(self) -> None:
        if not self.fixture_version or len(self.fixture_version) > 128 or not re.fullmatch(r'[0-9a-f]{64}', self.fixture_sha256):
            raise ValueError('fixed fixture provenance is required')

    def query(self, statement: str, parameters: list[Any], *, timeout_s: float) -> dict[str, Any]:
        if type(timeout_s) not in {int, float} or not math.isfinite(timeout_s) or not 0 < timeout_s <= 300:
            raise ValueError('invalid query timeout')
        deadline = time.monotonic() + timeout_s
        normalized = self.policy.validate(statement, parameters)
        # A trusted server-side limit is additional to the fetch/output bound.
        tree = cast(exp.Select, parse_one(normalized, read='postgres'))
        limit = tree.args.get('limit')
        if limit is None or int(limit.expression.this) > self.limits.max_rows:
            tree = tree.limit(self.limits.max_rows + 1)
        executed = tree.sql(dialect='postgres')
        try:
            if time.monotonic() >= deadline:
                raise TimeoutError('readonly query deadline exhausted before connection')
            connect_timeout = min(self.limits.connect_timeout_s, max(1, math.ceil(deadline - time.monotonic())))
            with psycopg.connect(**self.credentials.connection_kwargs(connect_timeout), autocommit=True) as conn:
                # Credentials are independently limited, not a control-plane login
                # with a Python-only role label or a read-only setting.
                flags = conn.execute('SELECT rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_roles WHERE rolname=current_user').fetchone()
                if flags is None or any(flags):
                    raise PolicyDenied('elevated credentials are not a reader capability')
                if conn.execute("SELECT pg_has_role(current_user,'mgar_reader','member')").fetchone() == (True,):
                    raise PolicyDenied('control-plane reader membership is forbidden')
                writable = conn.execute("SELECT has_schema_privilege(current_user,%s,'CREATE') OR has_table_privilege(current_user,%s,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')", [self.policy.schema, self.policy.schema + '.orders']).fetchone()
                if writable is None or writable[0]:
                    raise PolicyDenied('writer credentials are not a reader capability')
                with conn.transaction():
                    conn.execute('SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY')
                    remaining_ms = max(1, int((deadline - time.monotonic()) * 1000))
                    if time.monotonic() >= deadline:
                        raise TimeoutError('readonly query deadline exhausted')
                    statement_ms = min(self.limits.statement_timeout_ms, remaining_ms)
                    lock_ms = min(self.limits.lock_timeout_ms, remaining_ms)
                    conn.execute("SELECT set_config('statement_timeout',%s,true),set_config('lock_timeout',%s,true),set_config('search_path','pg_catalog',true)", [str(statement_ms), str(lock_ms)])
                    snapshot = conn.execute('SELECT pg_current_snapshot()::text').fetchone()
                    with conn.cursor() as cur:
                        cur.execute(executed, parameters if parameters else None)
                        description = cur.description
                        if description is None or snapshot is None:
                            raise PolicyDenied('SELECT observation missing')
                        columns = [{'name': item.name, 'type_oid': item.type_code} for item in description]
                        if len({c['name'] for c in columns}) != len(columns):
                            raise PolicyDenied('duplicate result column labels are forbidden')
                        rows = [[_cell(cell) for cell in row] for row in cur.fetchmany(self.limits.max_rows + 1)]
                    if len(rows) > self.limits.max_rows:
                        raise PolicyDenied('SQL row limit exceeded')
                    result: dict[str, Any] = {'schema_version': 'sql_observation.v1', 'statement': normalized,
                        'executed_statement': executed, 'parameters': parameters.copy(), 'snapshot_id': snapshot[0],
                        'fixture_version': self.fixture_version, 'fixture_sha256': self.fixture_sha256,
                        'columns': columns, 'rows': rows, 'row_count': len(rows), 'verified': False}
                    encoded = canonical_json(result)
                    result['content_sha256'] = hashlib.sha256(encoded.encode()).hexdigest()
                    if len(canonical_json(result).encode()) > self.limits.max_output_bytes:
                        raise PolicyDenied('SQL observation output limit exceeded')
                    if time.monotonic() >= deadline:
                        raise TimeoutError('readonly query deadline exhausted')
                    return result
        except (psycopg.errors.QueryCanceled, psycopg.errors.LockNotAvailable):
            raise TimeoutError('PostgreSQL readonly resource deadline exhausted') from None
        except (PolicyDenied, TimeoutError):
            raise
        except psycopg.Error:
            # Driver exceptions may contain connection details or query text.
            raise PolicyDenied('guarded readonly database operation denied') from None

    def tools(self) -> ToolRegistry:
        def query(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
            return self.query(arguments['sql'], arguments['parameters'], timeout_s=timeout_s)
        return ToolRegistry([ToolSpec('data.query', {
            'type': 'object', 'additionalProperties': False, 'required': ['sql', 'parameters'],
            'properties': {'sql': {'type': 'string', 'minLength': 1, 'maxLength': 8192},
                           'parameters': {'type': 'array', 'maxItems': 16, 'items': {'type': ['string', 'number', 'boolean', 'null']}}}},
            query, frozenset({'data'}), scope=self.policy.schema + '.orders',
            schema_version='readonly_sql.v1', timeout_s=5, max_output_bytes=self.limits.max_output_bytes)])
