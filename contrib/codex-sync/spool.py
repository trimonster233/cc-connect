"""Private filesystem transport for containers without a host socket mount."""
import json
import os
from pathlib import Path
import time
import uuid

class SpoolConnection:
    def __init__(self, path):
        self.path = Path(path)
        self.timeout = 6
        self.sock = self
        self.identifier = uuid.uuid4().hex
    def settimeout(self, timeout):
        self.timeout = timeout
    def request(self, method, path, body, headers):
        self.path.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.request_path = self.path / (self.identifier + '.request')
        self.response_path = self.path / (self.identifier + '.response')
        temporary = self.path / (self.identifier + '.tmp')
        with temporary.open('x') as stream:
            os.chmod(temporary, 0o600)
            json.dump({'id': self.identifier, 'path': path, 'body': json.loads(body)}, stream)
        os.replace(temporary, self.request_path)
    def getresponse(self):
        deadline = time.monotonic() + self.timeout
        while time.monotonic() < deadline:
            try:
                self.response = json.loads(self.response_path.read_text())
                self.status = self.response['status']
                return self
            except FileNotFoundError:
                time.sleep(0.1)
        raise TimeoutError('container relay response timed out')
    def read(self):
        return json.dumps(self.response['body']).encode()
    def close(self):
        for suffix in ('.request', '.response', '.tmp'):
            (self.path / (self.identifier + suffix)).unlink(missing_ok=True)
