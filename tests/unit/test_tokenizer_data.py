"""Synthetic dependency archives only; no provider request or token-count claim."""
import hashlib
import io
from pathlib import Path
import urllib.error
import zipfile

import pytest

from eval.live.tokenizer_data import OFFICIAL_URL, audit_archive, fetch


def archive(items):
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w', compression=zipfile.ZIP_DEFLATED) as z:
        for name, body in items:
            z.writestr(name, body)
    return out.getvalue()


def test_tokenizer_archive_audit_is_not_counter_proof():
    data = archive([('tokenizer/tokenizer.json', b'{"test_fixture":true}'),
                    ('tokenizer/demo.py', b'raise RuntimeError("never execute")')])
    result = audit_archive(data)
    assert result['archive_sha256'] == hashlib.sha256(data).hexdigest()
    assert result['status'] == 'ARCHIVE_AUDITED_COUNTER_UNVERIFIED'
    assert result['executable_code_present'] is True
    assert result['files'][0]['sha256'] == hashlib.sha256(b'{"test_fixture":true}').hexdigest()


@pytest.mark.parametrize('name', ['../key', '/absolute', 'C:/key', 'dir\\key', 'a/../../key', 'a//b'])
def test_tokenizer_archive_unsafe_paths_rejected(name):
    with pytest.raises(ValueError, match='UNSAFE_ARCHIVE_PATH'):
        audit_archive(archive([(name, b'{}')]))


def test_tokenizer_archive_symlinks_rejected():
    link = zipfile.ZipInfo('tokenizer/link')
    link.create_system = 3
    link.external_attr = 0o120777 << 16
    with pytest.raises(ValueError, match='UNSAFE_ARCHIVE_MEMBER'):
        audit_archive(archive([(link, b'../key')]))


def test_tokenizer_archive_duplicate_case_alias_rejected():
    with pytest.raises(ValueError, match='DUPLICATE_ARCHIVE_PATH'):
        audit_archive(archive([('vocab.json', b'{}'), ('VOCAB.json', b'{}')]))


def test_tokenizer_archive_compression_bomb_rejected():
    with pytest.raises(ValueError, match='ARCHIVE_LIMIT'):
        audit_archive(archive([('data', b'A' * (2 * 1024 * 1024))]))


def test_tokenizer_archive_invalid_zip_rejected():
    with pytest.raises(ValueError, match='INVALID_ARCHIVE'):
        audit_archive(b'not a zip')


def test_tokenizer_archive_corrupted_member_rejected():
    raw = bytearray(archive([('tokenizer.json', b'{"fixture":true}')]))
    with zipfile.ZipFile(io.BytesIO(raw)) as z:
        member = z.infolist()[0]
        start = member.header_offset + 30 + len(member.filename.encode()) + len(member.extra)
    raw[start] ^= 0xff
    with pytest.raises(ValueError, match='INVALID_ARCHIVE'):
        audit_archive(bytes(raw))


def test_tokenizer_fetch_failure_does_not_enable_counter(tmp_path, monkeypatch):
    class Denied:
        def open(self, request, timeout):
            assert request.full_url == OFFICIAL_URL
            assert request.get_header('Authorization') is None
            raise urllib.error.URLError(OSError('Tunnel connection failed: 403 Forbidden'))
    monkeypatch.setattr('urllib.request.build_opener', lambda *args: Denied())
    result = fetch(tmp_path / '.local/download', root=tmp_path)
    assert result['status'] == 'BLOCKED' and result['http_status'] == 403
    assert not (tmp_path / '.local/download/tokenizer.zip').exists()
    assert result['generation_requests_sent'] == 0


def test_tokenizer_fetch_reads_public_bytes_without_credentials(tmp_path, monkeypatch):
    raw = archive([('tokenizer.json', b'{}')])
    monkeypatch.setenv('DEEPSEEK_API_KEY', 'synthetic-do-not-send')
    class Response(io.BytesIO):
        status = 200
    class Server:
        def open(self, request, timeout):
            assert request.full_url == OFFICIAL_URL
            assert request.get_header('Authorization') is None
            return Response(raw)
    monkeypatch.setattr('urllib.request.build_opener', lambda *args: Server())
    result = fetch(tmp_path / '.local/download', root=tmp_path)
    assert result['status'] == 'ARCHIVE_AUDITED_COUNTER_UNVERIFIED'
    assert (tmp_path / '.local/download/tokenizer.zip').read_bytes() == raw
    assert 'synthetic-do-not-send' not in (tmp_path / '.local/download/fetch.json').read_text()


@pytest.mark.parametrize('http_status', [201, 206])
def test_tokenizer_fetch_requires_full_success_response(tmp_path, monkeypatch, http_status):
    raw = archive([('tokenizer.json', b'{}')])
    class Response(io.BytesIO):
        status = http_status
    class Server:
        def open(self, request, timeout):
            return Response(raw)
    monkeypatch.setattr('urllib.request.build_opener', lambda *args: Server())
    result = fetch(tmp_path / '.local/download', root=tmp_path)
    assert result['status'] == 'BLOCKED'
    assert result['counter_verified'] is False
    assert not (tmp_path / '.local/download/tokenizer.zip').exists()


def test_tokenizer_fetch_cannot_overwrite_or_follow_symlink(tmp_path):
    folder = tmp_path / '.local'
    folder.mkdir()
    (folder / 'download').mkdir()
    with pytest.raises(ValueError, match='OUTPUT_EXISTS'):
        fetch(folder / 'download', root=tmp_path)
    (folder / 'link').symlink_to(folder / 'download', target_is_directory=True)
    with pytest.raises(ValueError, match='UNSAFE_OUTPUT'):
        fetch(folder / 'link', root=tmp_path)


def test_tokenizer_fetch_output_is_local_only(tmp_path):
    with pytest.raises(ValueError, match='UNSAFE_OUTPUT'):
        fetch(Path('/tmp/external-tokenizer'), root=tmp_path)
