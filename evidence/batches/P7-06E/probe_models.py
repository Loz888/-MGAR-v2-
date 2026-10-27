"""Credential-safe read-only model availability probe; no generation."""
from datetime import datetime, timezone
import json
import os
import urllib.error
import urllib.request

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

result = {'method': 'GET', 'url': 'https://api.deepseek.com/models',
          'checked_at': datetime.now(timezone.utc).isoformat(), 'generation_requests': 0}
try:
    req = urllib.request.Request(result['url'], headers={'Authorization': 'Bearer ' + os.environ['DEEPSEEK_API_KEY']})
    with urllib.request.build_opener(NoRedirect).open(req, timeout=15) as response:
        result['http_status'] = response.status
        body = response.read(65537)
        if len(body) > 65536:
            raise ValueError('response_limit')
        data = json.loads(body)
        result['model_ids'] = [r['id'] for r in data['data']]
        result['approved_api_model_present'] = 'deepseek-flash' in result['model_ids']
except urllib.error.HTTPError as error:
    result.update(http_status=error.code, error_type=type(error).__name__)
except (urllib.error.URLError, KeyError, ValueError, OSError) as error:
    result['error_type'] = type(error).__name__
print(json.dumps(result))
raise SystemExit(0 if result.get('http_status') == 200 and result.get('approved_api_model_present') else 2)
