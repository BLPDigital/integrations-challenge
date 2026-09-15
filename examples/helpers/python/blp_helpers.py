"""Zero-signal plumbing for the BLP integrations exercise.

Copy it, rewrite it, or ignore it. None of it is graded.
"""

from __future__ import annotations

import hashlib
import json
import os
import urllib.error
import urllib.request


class ApiError(Exception):
    """The canonical error body both servers answer with."""

    def __init__(self, status: int, body: dict):
        self.status = status
        self.code = body.get("code", "HTTP_%d" % status)
        self.retriable = bool(body.get("retriable", False))
        self.retry_after_hint_ms = body.get("retry_after_hint_ms", 0)
        self.details = body.get("details", {})
        super().__init__("%d %s" % (status, self.code))


class Client:
    """One authenticated HTTP surface: the ERP or the twin.

    Two behaviors here matter and both are published in docs/spec.md: the token
    is refreshed on a 401 and the request is replayed once, and a failure is
    retried ONLY when the server said it is retriable, at most three attempts.
    """

    MAX_ATTEMPTS = 3

    def __init__(self, base_url, client_id, client_secret, token_path):
        self.base = base_url.rstrip("/")
        self.client_id = client_id
        self.client_secret = client_secret
        self.token_path = token_path
        self.token = None
        self.requests = 0

    def authenticate(self):
        body = json.dumps({"client_id": self.client_id,
                           "client_secret": self.client_secret}).encode()
        req = urllib.request.Request(self.base + self.token_path, data=body,
                                     method="POST",
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req) as resp:
            self.token = json.load(resp)["access_token"]
        self.requests += 1

    def call(self, method, path, body=None, headers=None,
             content_type="application/json"):
        """Perform one request, refreshing the token and retrying where allowed."""
        if self.token is None:
            self.authenticate()
        attempt = 0
        while True:
            attempt += 1
            h = {"Authorization": "Bearer " + self.token}
            if body is not None:
                h["Content-Type"] = content_type
            h.update(headers or {})
            req = urllib.request.Request(self.base + path, data=body,
                                         method=method, headers=h)
            try:
                with urllib.request.urlopen(req) as resp:
                    self.requests += 1
                    raw = resp.read()
                    return resp.status, dict(resp.headers), (json.loads(raw) if raw else None)
            except urllib.error.HTTPError as e:
                self.requests += 1
                raw = e.read()
                try:
                    parsed = json.loads(raw) if raw else {}
                except ValueError:
                    parsed = {}
                if e.code == 207:
                    # Multi-Status is an answer, not a failure: the caller wants
                    # the per-item results.
                    return e.code, dict(e.headers), parsed
                err = ApiError(e.code, parsed if isinstance(parsed, dict) else {})
                if e.code == 401 and attempt < self.MAX_ATTEMPTS:
                    self.authenticate()
                    continue
                if err.retriable and attempt < self.MAX_ATTEMPTS:
                    continue
                raise err


def idempotency_key(tenant, proposal_id, content_hash):
    """The conforming recipe from docs/spec.md.

    The asserted property is that the key is a pure function of the proposal and
    stable across runs. The run id must NOT be in here: a resumed run would then
    post everything a second time, and the ERP counts every such attempt.
    """
    return "blp:%s:%s:%s" % (tenant, proposal_id, content_hash[:16])


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while True:
            chunk = f.read(1 << 20)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


def publish_batch(inbox_root, batch_id, write_files):
    """The atomic drop protocol: staging directory, fsync, one rename.

    write_files(directory) writes the manifest and the data files.
    """
    incoming = os.path.join(inbox_root, "incoming")
    staging = os.path.join(incoming, ".staging-" + batch_id)
    final = os.path.join(incoming, batch_id)
    os.makedirs(staging, exist_ok=True)
    write_files(staging)
    for name in os.listdir(staging):
        fd = os.open(os.path.join(staging, name), os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    os.rename(staging, final)
    return final
