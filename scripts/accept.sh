#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ "${1:-}" =~ ^P[0-8]-[0-9][0-9]$ ]] || exit 2
python - "$1" <<'PY'
import sys,json,hashlib,datetime,subprocess,os
from pathlib import Path
sys.path.insert(0,"scripts")
from source_fingerprint import fingerprint
card=sys.argv[1]
run=Path('.local/offline-current').read_text().strip();p=Path(run)
if not p.resolve().is_relative_to(Path('.local').resolve()):raise SystemExit('invalid report path')
report=json.loads((p/'report.json').read_text())
if report['status']!='PASS' or (p/'fingerprint').read_text().strip()!=fingerprint():raise SystemExit('missing, stale or failed test run')
out=Path('evidence/batches')/card;out.mkdir(parents=True,exist_ok=True)
artifacts=[]
for f in p.iterdir():
    target=out/('offline-'+f.name+'.json')
    target.write_text(json.dumps({'raw_text':f.read_text()},ensure_ascii=False,indent=2)+'\n')
    artifacts.append({'path':str(target),'sha256':hashlib.sha256(target.read_bytes()).hexdigest()})
summary={'card_id':card,'status':'PASS','git_commit':None,'base_git_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'scope':'offline enabled suites only; task-specific assertions must also pass','environment':{'os':sys.platform,'go':subprocess.check_output(['go','version'],text=True).strip(),'python':sys.version.split()[0]},'test_summary':report,'positive_assertions':[{'name':'all_enabled_tests_completed','passed':True}],'negative_assertions':[],'known_limitations':['No paid model or production operations; this report does not replace task-specific acceptance.'],'unverified':[],'commands':[{'command':'bash scripts/test.sh --offline','exit_code':0,'log_path':str(out/'offline-report.json.json')}],'artifacts':artifacts,'timestamp_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
(out/'offline-accept.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'card_id':card,'status':summary['status'],'test_summary':report}))
PY
