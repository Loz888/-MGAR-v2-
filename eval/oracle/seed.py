"""Owned disposable synthetic PostgreSQL inputs, with a separate reader login."""
from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field
import os
import re
import secrets
from urllib.parse import parse_qs, unquote, urlsplit

import psycopg
from psycopg import sql

from reference import validate_input


def admin_kwargs():
    try:
        u = urlsplit(os.environ.get("MGAR_TEST_DATABASE_URL", ""))
        params = parse_qs(u.query, keep_blank_values=True)
        if (u.scheme not in {"postgres", "postgresql"} or u.hostname not in {"127.0.0.1", "localhost"}
                or u.port != 55432 or not re.fullmatch(r"mgar_test(?:_[0-9a-f]{16})?", u.path[1:])
                or set(params) - {"sslmode", "connect_timeout"} or any(len(v) != 1 for v in params.values())
                or not u.username or not u.password):
            raise ValueError()
        return {"host": "127.0.0.1", "hostaddr": "127.0.0.1", "port": 55432, "dbname": u.path[1:],
                "user": unquote(u.username), "password": unquote(u.password), "sslmode": "disable",
                "connect_timeout": 3, "options": "-c default_transaction_read_only=off"}
    except Exception:
        raise ValueError("SYNTHETIC_LOCAL_DATABASE_REQUIRED") from None


@dataclass(repr=False)
class Database:
    schema: str
    role: str
    _reader: dict = field(repr=False)

    def connect_reader(self):
        return psycopg.connect(**self._reader)

    def rows(self):
        with self.connect_reader() as connection:
            return connection.execute(sql.SQL("SELECT event_id,order_id,status,amount FROM {}.orders ORDER BY event_id").format(sql.Identifier(self.schema))).fetchall()


@contextmanager
def seeded(data):
    validate_input(data)
    kwargs = admin_kwargs()
    schema = "mgar_fixture_" + secrets.token_hex(8)
    role = "mgar_oracle_reader_" + secrets.token_hex(8)
    password = secrets.token_hex(24)
    admin = psycopg.connect(**kwargs, autocommit=True)
    have_schema = have_role = False
    try:
        admin.execute(sql.SQL("CREATE SCHEMA {}").format(sql.Identifier(schema)))
        have_schema = True
        admin.execute(sql.SQL("CREATE ROLE {} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD {}").format(sql.Identifier(role), sql.Literal(password)))
        have_role = True
        admin.execute(sql.SQL("ALTER ROLE {} SET default_transaction_read_only=on").format(sql.Identifier(role)))
        admin.execute(sql.SQL("ALTER ROLE {} SET search_path=pg_catalog").format(sql.Identifier(role)))
        admin.execute(sql.SQL("CREATE TABLE {}.orders(event_id integer PRIMARY KEY,order_id integer NOT NULL,status text NOT NULL CHECK(status IN ('paid','pending')),amount numeric(10,2) NOT NULL CHECK(amount>=0))").format(sql.Identifier(schema)))
        with admin.cursor() as cur:
            cur.executemany(sql.SQL("INSERT INTO {}.orders VALUES(%s,%s,%s,%s)").format(sql.Identifier(schema)),
                            [(r["event_id"], r["order_id"], r["status"], r["amount"]) for r in data["orders"]])
        admin.execute(sql.SQL("GRANT USAGE ON SCHEMA {} TO {}").format(sql.Identifier(schema), sql.Identifier(role)))
        admin.execute(sql.SQL("GRANT SELECT ON {}.orders TO {}").format(sql.Identifier(schema), sql.Identifier(role)))
        reader = dict(kwargs, user=role, password=password, options="-c default_transaction_read_only=on")
        yield Database(schema, role, reader)
    finally:
        if have_schema:
            admin.execute(sql.SQL("DROP SCHEMA {} CASCADE").format(sql.Identifier(schema)))
        if have_role:
            admin.execute(sql.SQL("DROP ROLE {}").format(sql.Identifier(role)))
        admin.close()
