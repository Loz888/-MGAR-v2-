"""Closed PostgreSQL SELECT grammar for the synthetic order table."""
from __future__ import annotations

from dataclasses import dataclass
import re
from typing import Any

import sqlglot
from sqlglot import exp
from sqlglot.errors import ErrorLevel

from mgar_agents.core.schemas import canonical_json
from mgar_agents.core.tools import PolicyDenied

ALLOWED_NODES = frozenset({
    exp.Select, exp.Alias, exp.Column, exp.Identifier, exp.From, exp.Table, exp.Star, exp.Literal,
    exp.Placeholder, exp.Where, exp.Group, exp.Having, exp.Order, exp.Ordered, exp.Limit,
    exp.Count, exp.Sum, exp.Avg, exp.Min, exp.Max, exp.Distinct,
    exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE, exp.And, exp.Or, exp.Not,
    exp.Paren, exp.Add, exp.Sub, exp.Mul, exp.Div, exp.Mod, exp.Is, exp.Null, exp.Boolean,
    exp.Neg, exp.Like, exp.ILike, exp.In, exp.Between,
})


@dataclass(frozen=True)
class SQLPolicy:
    schema: str
    tables: tuple[str, ...] = ('orders',)

    def __post_init__(self) -> None:
        if type(self.schema) is not str or not re.fullmatch(r'mgar_fixture_[0-9a-f]{16}', self.schema) or self.tables != ('orders',):
            raise ValueError('only an explicitly scoped synthetic orders table is supported')

    def validate(self, statement: str, parameters: list[Any]) -> str:
        try:
            if type(statement) is not str or not 1 <= len(statement.encode()) <= 8192 or statement.count('(') > 32:
                raise ValueError('SQL size/depth limit')
            if type(parameters) is not list or len(parameters) > 16:
                raise ValueError('SQL parameter bound')
            canonical_json(parameters)
            if any(type(p) not in {str, bool, int, float, type(None)} or (type(p) is str and len(p) > 4096) for p in parameters):
                raise ValueError('SQL parameters must be bounded JSON scalars')
            parsed = sqlglot.parse(statement, read='postgres', error_level=ErrorLevel.RAISE)
            if len(parsed) != 1 or type(parsed[0]) is not exp.Select:
                raise ValueError('one SELECT required')
            tree = parsed[0]
            nodes = list(tree.walk())
            if len(nodes) > 256 or sum(type(n) is exp.Select for n in nodes) != 1:
                raise ValueError('query shape bound')
            if any(type(n) not in ALLOWED_NODES or n.comments for n in nodes):
                raise ValueError('query contains an unsupported operation or comment')
            tables = list(tree.find_all(exp.Table))
            if len(tables) != 1 or tables[0].name != 'orders' or tables[0].db != self.schema or tables[0].catalog:
                raise ValueError('table is outside the permitted fixture')
            aliases = {n.alias for n in tree.find_all(exp.Alias)}
            columns = {'event_id', 'order_id', 'status', 'amount'} | aliases
            for node in nodes:
                if isinstance(node, exp.Identifier) and not re.fullmatch(r'[a-z_][a-z0-9_]{0,62}', node.name):
                    raise ValueError('unsupported SQL identifier')
                if isinstance(node, exp.Column) and (node.name not in columns or node.table not in {'', 'orders'} or node.db or node.catalog):
                    raise ValueError('column outside the permitted table')
            placeholders = list(tree.find_all(exp.Placeholder))
            if len(placeholders) != len(parameters) or any(n.this is not None for n in placeholders):
                raise ValueError('positional %s parameter arity required')
            limit = tree.args.get('limit')
            if limit is not None:
                value = limit.expression
                if type(value) is not exp.Literal or not value.is_int or not 0 <= int(value.this) <= 100:
                    raise ValueError('query row limit bound')
            return tree.sql(dialect='postgres')
        except TimeoutError:
            raise
        except Exception:
            raise PolicyDenied('SQL does not match the closed read-only policy') from None
