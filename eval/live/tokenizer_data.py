"""Acquire/audit the public tokenizer dependency; does not prove input counts."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import re
import ssl
import stat
import sys
import urllib.error
import urllib.request
import zipfile
import zlib

ROOT = Path(__file__).resolve().parents[2]
OFFICIAL_URL = 'https://cdn.deepseek.com/api-docs/deepseek_v4_tokenizer.zip'
MAX_ARCHIVE = 32 * 1024 * 1024
MAX_MEMBER = 64 * 1024 * 1024
MAX_TOTAL = 128 * 1024 * 1024
OFFICIAL_TEXT_BOUND = {
    'method': 'utf8-byte-bpe-chat.v1',
    'archive_sha256': 'e7310d1dafe0a86d8a5629fe78a7c763760f651db9b8682718a1781dcd6fe495',
    'tokenizer_sha256': '89085f12ef79460ac5f66d1119325ddfc694b4ab209d80bbd81d35f081dc9614',
    'config_sha256': '841f8cf146e3f0ad1082594a31f68ecf7608c20467ef355081333d85bbaeb1cb',
    'template_sha256': '3b8267e54b67df6500f231ffa969955cc9d26b3444a291ced4eb96ca7d3ca1ea',
    'request_mode': 'two-text-messages.non-thinking.no-native-format.v1',
}


def proof_fingerprint(proof):
    if proof != OFFICIAL_TEXT_BOUND:
        raise ValueError('INVALID_INPUT_BOUND_PROOF')
    return hashlib.sha256('\n'.join(proof[key] for key in OFFICIAL_TEXT_BOUND).encode()).hexdigest()


def audit_archive(data):
    """Read bounded bytes, verify CRC/hash, never extract or execute ZIP members."""
    if len(data) > MAX_ARCHIVE:
        raise ValueError('ARCHIVE_LIMIT')
    files, names, total = [], set(), 0
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            if not 1 <= len(archive.infolist()) <= 128:
                raise ValueError('ARCHIVE_LIMIT')
            for member in archive.infolist():
                name = member.filename
                stripped = name[:-1] if name.endswith('/') else name
                if (not stripped or len(name) > 256 or name.startswith('/') or ':' in name
                        or '\\' in name or member.orig_filename != name
                        or any(part in ['', '.', '..'] for part in stripped.split('/'))):
                    raise ValueError('UNSAFE_ARCHIVE_PATH')
                key = stripped.casefold()
                if key in names:
                    raise ValueError('DUPLICATE_ARCHIVE_PATH')
                names.add(key)
                mode = stat.S_IFMT(member.external_attr >> 16)
                if mode not in [0, stat.S_IFREG, stat.S_IFDIR] or member.flag_bits & 1:
                    raise ValueError('UNSAFE_ARCHIVE_MEMBER')
                total += member.file_size
                if (member.file_size > MAX_MEMBER or total > MAX_TOTAL
                        or member.file_size > 1024 * 1024 and member.file_size > max(member.compress_size, 1) * 1000):
                    raise ValueError('ARCHIVE_LIMIT')
                if member.is_dir():
                    continue
                with archive.open(member) as stream:
                    body = stream.read(MAX_MEMBER + 1)
                if len(body) != member.file_size:
                    raise ValueError('INVALID_ARCHIVE_SIZE')
                files.append({'name': name, 'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()})
    except (zipfile.BadZipFile, NotImplementedError, RuntimeError, EOFError, zlib.error) as error:
        raise ValueError('INVALID_ARCHIVE') from error
    if not files:
        raise ValueError('INVALID_ARCHIVE_EMPTY')
    return {'status': 'ARCHIVE_AUDITED_COUNTER_UNVERIFIED',
            'archive_sha256': hashlib.sha256(data).hexdigest(), 'files': files,
            'executable_code_present': any(Path(row['name']).suffix.lower() in ['.py', '.so', '.dll', '.exe', '.sh'] for row in files),
            'counter_verified': False}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def fetch(output, *, root=ROOT):
    output, root = Path(output).absolute(), Path(root).absolute()
    if ('..' in output.parts or not output.is_relative_to(root / '.local')
            or any(p.is_symlink() for p in [output, *output.parents])):
        raise ValueError('UNSAFE_OUTPUT')
    if output.exists():
        raise ValueError('OUTPUT_EXISTS')
    output.mkdir(parents=True, exist_ok=False)
    result = {'url': OFFICIAL_URL, 'timestamp_utc': datetime.now(timezone.utc).isoformat(),
              'status': 'BLOCKED', 'generation_requests_sent': 0, 'counter_verified': False}
    try:
        opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context()))
        # No key or Authorization header: this is a public dependency, on its
        # own configured host. Keep inherited proxy/CA and refuse redirects.
        request = urllib.request.Request(OFFICIAL_URL, method='GET')
        with opener.open(request, timeout=25) as response:
            result['http_status'] = response.status
            if response.status != 200:
                raise ValueError('FULL_RESPONSE_REQUIRED')
            data = response.read(MAX_ARCHIVE + 1)
        result.update(audit_archive(data))
        with (output / 'tokenizer.zip').open('xb') as stream:
            stream.write(data)
    except urllib.error.HTTPError as error:
        result.update(error_type='HTTP_ERROR', http_status=error.code)
    except urllib.error.URLError as error:
        result.update(error_type='URLError', transport_reason_type=type(error.reason).__name__)
        # Only retain a numeric tunnel status; never log arbitrary error text,
        # proxy credentials, headers, or a response body.
        match = re.search(r'Tunnel connection failed:\s*(\d{3})\b', str(error.reason))
        if match:
            result['http_status'] = int(match[1])
    except (ValueError, OSError) as error:
        result.update(status='BLOCKED', error_type=type(error).__name__)
    with (output / 'fetch.json').open('x') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path, help='new directory below the checkout .local')
    args = parser.parse_args()
    try:
        result = fetch(args.output)
    except (OSError, ValueError) as error:
        print(json.dumps({'status': 'BLOCKED', 'error_type': type(error).__name__}))
        return 2
    print(json.dumps(result, ensure_ascii=False))
    return 2 if result['status'] == 'BLOCKED' else 0


if __name__ == '__main__':
    sys.exit(main())
