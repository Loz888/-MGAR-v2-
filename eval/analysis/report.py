"""Generate reproducible statistics from frozen raw experiment evidence."""
import argparse
import csv
import json
import os
from pathlib import Path
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from eval.analysis.inputs import load, local_path  # noqa: E402
from eval.analysis.metrics import paired_delta, summarize, usage_summary  # noqa: E402
from eval.runner import digest  # noqa: E402

ARMS = ['B0', 'B1', 'B2', 'B3', 'B4', 'A1', 'A2']
COMPARISONS = [('B0', 'B1'), ('B1', 'B2'), ('B2', 'B3'), ('B3', 'B4'), ('A1', 'B4'), ('A2', 'B4')]


def flatten(value, prefix=''):
    result = {}
    for key, item in value.items():
        name = prefix + key
        if isinstance(item, dict):
            result.update(flatten(item, name + '.'))
        elif isinstance(item, list):
            result[name] = json.dumps(item, sort_keys=True)
        else:
            result[name] = item if item is not None else 'N/A'
    return result


def csv_file(path, rows):
    flat = [flatten(row) for row in rows]
    with path.open('x', newline='') as stream:
        writer = csv.DictWriter(stream, fieldnames=sorted({k for row in flat for k in row}), restval='N/A')
        writer.writeheader()
        writer.writerows(flat)


def display(value):
    if value is None:
        return 'N/A'
    return f'{value:.4f}' if isinstance(value, float) else str(value)


def generate(input_dir, output):
    output = local_path(output)
    if output.exists():
        raise ValueError('OUTPUT_ALREADY_EXISTS')
    rows, provenance = load(input_dir)
    arms = {arm: summarize([r for r in rows if r['arm'] == arm]) for arm in ARMS}
    categories = [{'arm': arm, 'category': cat, **summarize([r for r in rows if r['arm'] == arm and r['category'] == cat])}
                  for arm in ARMS for cat in sorted({r['category'] for r in rows})]
    per_case = [{k: v for k, v in r.items() if k != 'models'} | {'usage': usage_summary(r['models'])} for r in rows]
    paired = []
    for left, right in COMPARISONS:
        for metric in ['oracle_pass', 'latency_ms', 'input_tokens', 'output_tokens', 'estimated_cost_usd']:
            values = {}
            for arm in [left, right]:
                values[arm] = {r['case_id']: (int(r[metric]) if metric == 'oracle_pass' else
                                            r[metric] if metric == 'latency_ms' else r['usage'][metric])
                               for r in per_case if r['arm'] == arm}
            missing = any(v is None for group in values.values() for v in group.values())
            delta = {'pairs': 0, 'delta': None, 'ci95': None, 'reason': 'missing usage; no pair dropping'} if missing else paired_delta(values[left], values[right])
            paired.append({'left': left, 'right': right, 'metric': metric, **delta})
    result = {'schema_version': 'experiment-analysis.v1', 'evidence_level': 'E0_MOCK_ENGINEERING',
              'assigned': len(rows), 'paid_calls': 0, 'arms': arms, 'categories': categories, 'paired': paired,
              'provenance': provenance, 'per_case': per_case,
              'limitations': ['Mock decisions are reviewed templates; no real model ability claim.',
                              'One run per case; sequential nonrandomized order; descriptive latency only.',
                              'Bootstrap is conditional on frozen cases, not model sampling uncertainty.',
                              'Tokens are local_stub UTF-8 bytes/4, cost uses demo-free.v1.',
                              'Strict selector records unavailable: retention N/A. Citation validity is identity, not semantic correctness.',
                              'B3 and A2 are independent runs of the same treatment.']}
    output.mkdir(parents=True)
    (output / 'metrics.json').write_text(json.dumps(result, sort_keys=True, ensure_ascii=False, indent=2, allow_nan=False) + '\n')
    csv_file(output / 'per_case.csv', per_case)
    csv_file(output / 'by_arm.csv', [{'arm': arm, **arms[arm]} for arm in ARMS])
    csv_file(output / 'by_category.csv', categories)
    csv_file(output / 'paired.csv', paired)
    lines = ['# E0 Mock engineering experiment analysis', '',
             'All assigned cases and failures retained. N/A means missing or inapplicable; never zero-filled.', '',
             '| Arm | N | Oracle pass | Rate | Unsupported answer rate | False Workflow success rate | Input tokens | Output tokens | Usage coverage | Cost USD | Experiment P50 ms | P95 ms | Critical retention | Role coverage |',
             '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|']
    for arm, s in arms.items():
        values = [arm, s['assigned'], s['oracle_pass'], s['task_success_rate'], s['unsupported_answer_claim_rate'],
                  s['oracle_false_workflow_success_rate'], s['usage']['input_tokens'], s['usage']['output_tokens'],
                  s['usage']['coverage'], s['usage']['estimated_cost_usd'], s['experiment_latency_p50_ms'],
                  s['experiment_latency_p95_ms'], s['critical_evidence_retention'], s['business_role_coverage']]
        lines.append('| ' + ' | '.join(display(v) for v in values) + ' |')
    lines += ['', '## Paired Oracle differences (right minus left)', '', '| Comparison | Pairs | Delta | 95% CI |', '|---|---:|---:|---|']
    for p in paired:
        if p['metric'] == 'oracle_pass':
            lines.append(f"| {p['right']} − {p['left']} | {p['pairs']} | {display(p['delta'])} | {p['ci95']} |")
    lines += ['', '## Limits', '', *['- ' + line for line in result['limitations']], '',
              'Full categories, per-case values, usage bounds and Workflow latency coverage are in the adjacent CSV/JSON files.',
              f"Input index SHA256: `{provenance['index_sha256']}`.", '']
    (output / 'report.md').write_text('\n'.join(lines))
    plot(output / 'comparison.svg', arms, paired)
    (output / 'files.sha256.json').write_text(json.dumps({p.name: digest(p.read_bytes()) for p in sorted(output.iterdir())}, indent=2) + '\n')
    return result


def plot(path, arms, paired):
    # A private writable cache; no user HOME mutation and no timestamp in SVG.
    with tempfile.TemporaryDirectory(prefix='mgar-plot-') as cache:
        previous = {k: os.environ.get(k) for k in ['MPLCONFIGDIR', 'XDG_CACHE_HOME']}
        os.environ.update(MPLCONFIGDIR=cache, XDG_CACHE_HOME=cache)
        try:
            import matplotlib
            matplotlib.use('Agg')
            import matplotlib.pyplot as plt
            with matplotlib.rc_context({'svg.hashsalt': 'mgar-analysis.v1', 'font.family': 'DejaVu Sans'}):
                fig, axes = plt.subplots(1, 2, figsize=(11, 4.5), layout='constrained')
                axes[0].bar(ARMS, [arms[a]['task_success_rate'] for a in ARMS], color='#347c9c')
                axes[0].set(ylabel='Oracle pass / all assigned cases', ylim=(0, 1), title='E0 mock engineering (60 cases / arm)')
                comparisons = [p for p in paired if p['metric'] == 'oracle_pass']
                for i, p in enumerate(comparisons):
                    if p['delta'] is not None:
                        axes[1].errorbar(p['delta'], i, xerr=[[p['delta']-p['ci95'][0]], [p['ci95'][1]-p['delta']]], fmt='o', color='#347c9c', capsize=4)
                axes[1].set_yticks(range(len(comparisons)), [p['right'] + ' - ' + p['left'] for p in comparisons])
                axes[1].axvline(0, color='grey', linewidth=1)
                axes[1].set(xlabel='Paired rate delta; bootstrap 95% CI', title='Conditional on frozen cases')
                fig.savefig(path, metadata={'Date': None, 'Creator': 'MGAR analysis.v1'})
                plt.close(fig)
        finally:
            for key, value in previous.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    result = generate(args.input, args.output)
    print(json.dumps({'assigned': result['assigned'], 'output': str(args.output), 'evidence_level': result['evidence_level']}))


if __name__ == '__main__':
    main()
