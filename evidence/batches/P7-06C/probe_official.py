"""Public official documentation + authenticated model-list checks; no generation."""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

class Text(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts, self.hidden = [], 0
    def handle_starttag(self, tag, attrs):
        if tag in ['script', 'style']:
            self.hidden += 1
    def handle_endtag(self, tag):
        if tag in ['script', 'style']:
            self.hidden = max(0, self.hidden - 1)
    def handle_data(self, data):
        if not self.hidden and data.strip():
            self.parts.append(data.strip())

out = Path(sys.argv[1])
out.mkdir(parents=True, exist_ok=False)

def fetch(item):
    name, url = item
    report = {'url': url, 'time_utc': datetime.now(timezone.utc).isoformat(), 'redirects': []}
    opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context()))
    try:
        for _ in range(4):
            headers = {'Accept': 'application/json' if name == 'models' else '*/*'}
            if name == 'models':
                key = os.environ.get('DEEPSEEK_API_KEY')
                if not key:
                    report['error'] = 'KEY_NOT_INJECTED'
                    return name, report
                headers['Authorization'] = 'Bearer ' + key
            try:
                response = opener.open(urllib.request.Request(url, headers=headers), timeout=25)
                break
            except urllib.error.HTTPError as error:
                if error.code not in [301, 302, 303, 307, 308] or name == 'models':
                    raise
                target = urllib.parse.urljoin(url, error.headers.get('Location', ''))
                parsed = urllib.parse.urlsplit(target)
                if parsed.scheme != 'https' or parsed.hostname != 'api-docs.deepseek.com' or parsed.username or parsed.password:
                    raise ValueError('REDIRECT_NOT_ALLOWED')
                report['redirects'].append({'status': error.code, 'url': target})
                url = target
        else:
            raise ValueError('REDIRECT_LIMIT')
        with response:
            body = response.read(8 * 1024 * 1024 + 1)
            if len(body) > 8 * 1024 * 1024:
                raise ValueError('RESPONSE_TOO_LARGE')
            report.update(http_status=response.status, final_url=response.url, sha256=hashlib.sha256(body).hexdigest())
        if name == 'models':
            report['model_ids'] = [m['id'] for m in json.loads(body)['data']]
        else:
            (out / (name + '.raw')).write_bytes(body)
            parser = Text()
            parser.feed(body.decode('utf-8'))
            (out / (name + '.txt')).write_text('\n'.join(parser.parts))
            report['artifact'] = name + '.raw'
    except urllib.error.HTTPError as error:
        report.update(http_status=error.code, error='HTTP_ERROR')
    except Exception as error:
        report.update(error=type(error).__name__)
    return name, report

urls = [('models', 'https://api.deepseek.com/models'),
        ('pricing-slash', 'https://api-docs.deepseek.com/quick_start/pricing/'),
        ('pricing-markdown', 'https://api-docs.deepseek.com/quick_start/pricing.md'),
        ('news261017', 'https://api-docs.deepseek.com/news/news261017'),
        ('site-main', 'https://api-docs.deepseek.com/assets/js/main.9486cded.js'),
        ('site-runtime', 'https://api-docs.deepseek.com/assets/js/runtime~main.201766f5.js')]
with ThreadPoolExecutor(max_workers=4) as executor:
    result = dict(executor.map(fetch, urls))
(out / 'fetch.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(result, ensure_ascii=False, indent=2))
