"""Deterministic descriptive statistics; missing evidence never becomes zero."""
import math
import random
from collections import Counter


def number(value, *, signed=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or (not signed and value < 0):
        raise ValueError('INVALID_NUMERIC_OBSERVATION')
    return value


def rate(numerator, denominator):
    number(numerator)
    number(denominator)
    if numerator > denominator:
        raise ValueError('INVALID_DENOMINATOR')
    return numerator / denominator if denominator else None


def percentile(values, percent, *, signed=False):
    number(percent)
    if percent > 100:
        raise ValueError('INVALID_PERCENTILE')
    values = sorted(number(v, signed=signed) for v in values)
    if not values:
        return None
    position = (len(values) - 1) * percent / 100
    lo, hi = math.floor(position), math.ceil(position)
    return values[lo] + (values[hi] - values[lo]) * (position - lo)


def paired_delta(left, right, *, seed=20261025):
    if left.keys() != right.keys():
        raise ValueError('MISSING_PAIRED_CASE')
    differences = [number(right[k], signed=True) - number(left[k], signed=True) for k in sorted(left)]
    result = {'pairs': len(differences), 'seed': seed, 'resamples': 5000, 'delta': None, 'ci95': None}
    if differences:
        rng = random.Random(seed)
        samples = [sum(rng.choices(differences, k=len(differences))) / len(differences) for _ in range(5000)]
        result.update(delta=sum(differences) / len(differences),
                      ci95=[percentile(samples, 2.5, signed=True), percentile(samples, 97.5, signed=True)])
    return result


def usage_summary(calls):
    fields = ['input_tokens', 'output_tokens', 'cost_micros']
    known, values, bounds = 0, dict.fromkeys(fields, 0), dict.fromkeys(fields, 0)
    for call in calls:
        usage, reserved = call.get('usage') or {}, call.get('reservation') or {}
        for source in [usage, reserved]:
            for field in fields:
                if source.get(field) is not None:
                    value = number(source[field])
                    if not isinstance(value, int):
                        raise ValueError('NONINTEGER_USAGE')
        complete = call.get('usage_known') is True and all(usage.get(f) is not None for f in fields)
        known += complete
        for field in fields:
            if complete:
                values[field] += usage[field]
            upper = usage.get(field) if complete else reserved.get(field)
            bounds[field] = bounds[field] + upper if bounds[field] is not None and upper is not None else None
    result = {'calls': len(calls), 'known_calls': known, 'coverage': rate(known, len(calls)),
              'sources': sorted({str(c.get('usage_source', 'unknown')) for c in calls})}
    for field in fields:
        key = 'estimated_cost_usd' if field == 'cost_micros' else field
        divisor = 1_000_000 if field == 'cost_micros' else 1
        result[key] = values[field] / divisor if known == len(calls) else None
        result[key + '_known'] = values[field] / divisor
        result[key + '_upper'] = bounds[field] / divisor if bounds[field] is not None else None
    return result


def summarize(rows):
    n = len(rows)
    passed = sum(r['oracle_pass'] is True for r in rows)
    claims = sum(r['success_claim'] is True for r in rows)
    unsupported = sum(r['success_claim'] is True and r['oracle_pass'] is not True for r in rows)
    workflows = [r['workflow_success'] for r in rows if r['workflow_success'] is not None]
    workflow_claims = sum(v is True for v in workflows)
    result = {'assigned': n, 'oracle_pass': passed, 'oracle_fail': n - passed,
              'task_success_rate': rate(passed, n), 'answer_claims': claims,
              'unsupported_answer_claims': unsupported, 'unsupported_answer_claim_rate': rate(unsupported, claims),
              'oracle_false_workflow_success_rate': rate(sum(r['workflow_success'] is True and not r['oracle_pass'] for r in rows), workflow_claims),
              'workflow_success_rate': rate(workflow_claims, len(workflows)), 'workflow_coverage': rate(len(workflows), n),
              'model_calls': sum(len(r['models']) for r in rows), 'tool_calls': sum(r['tool_calls'] for r in rows),
              'usage': usage_summary([c for r in rows for c in r['models']]),
              'failure_categories': dict(sorted(Counter(r.get('category', 'unspecified') for r in rows if not r['oracle_pass']).items()))}
    for source, label in [('latency_ms', 'experiment_latency'), ('workflow_ms', 'workflow_latency')]:
        values = [r[source] for r in rows if r[source] is not None]
        result[label + '_coverage'] = rate(len(values), n)
        for p in [50, 95]:
            result[f'{label}_p{p}_ms'] = percentile(values, p)
    for name, num, den in [('critical_evidence_retention', 'critical_admitted', 'critical_available'),
                           ('citation_identity_validity', 'citation_valid', 'citation_total'),
                           ('business_role_coverage', 'business_roles_retained', 'business_roles_required')]:
        result[name] = rate(sum(r[num] for r in rows), sum(r[den] for r in rows))
        result[name + '_observed_cases'] = sum(r[den] > 0 for r in rows)
    return result
