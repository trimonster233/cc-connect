#!/usr/bin/env python3
import json
import os
from pathlib import Path
import re
import sys
import threading
import time

root = Path(sys.argv[1])
root.mkdir(parents=True, exist_ok=True, mode=0o700)

def responses():
    for line in sys.stdin:
        item = json.loads(line)
        identifier = item['id']
        if not re.fullmatch('[a-f0-9]{32}', identifier):
            continue
        request = root / (identifier + '.request')
        if not request.exists():
            continue
        path = root / (identifier + '.response')
        temporary = root / (identifier + '.result')
        with temporary.open('w') as stream:
            os.chmod(temporary, 0o600)
            json.dump(item, stream)
        os.replace(temporary, path)
    os._exit(0)

threading.Thread(target=responses, daemon=True).start()
seen = set()
while True:
    for path in root.glob('*.request'):
        if path.name in seen:
            continue
        if time.time() - path.stat().st_mtime > 300:
            path.unlink(missing_ok=True)
            continue
        try:
            item = json.loads(path.read_text())
            print(json.dumps(item), flush=True)
            seen.add(path.name)
        except (OSError, ValueError):
            continue
    seen.intersection_update(path.name for path in root.glob('*.request'))
    time.sleep(0.2)
