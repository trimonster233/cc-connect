#!/usr/bin/env python3
"""Install a Bash wrapper that forwards cwd to an existing Codex app server."""
import argparse
from pathlib import Path
import re
import shlex
import shutil
import sys


def configure_shell(rcfile, codex_home, server_url, name):
    if not re.fullmatch(r'[A-Za-z0-9_-]+', name):
        raise ValueError('invalid environment name')
    marker = '# CC-Connect ' + name + ' shared server'
    end_marker = '# End CC-Connect ' + name + ' shared server'
    block = f'''{marker}
export CODEX_HOME={shlex.quote(str(codex_home))}
codex() {{
    # The shared server has its own cwd; forward this terminal's directory.
    local codex_arg
    local -a codex_cwd_args=(--cd "$PWD")
    for codex_arg in "$@"; do
        case "$codex_arg" in
            --) break ;;
            -C|--cd|-C?*|--cd=*)
                codex_cwd_args=()
                break
                ;;
        esac
    done
    command codex --remote {shlex.quote(server_url)} "${{codex_cwd_args[@]}}" "$@"
}}
{end_marker}
'''
    text = rcfile.read_text()
    if marker not in text:
        updated = text.rstrip('\n') + '\n\n' + block
    else:
        if end_marker in text:
            pattern = (r'(?ms)^' + re.escape(marker) + r'\n.*?^'
                       + re.escape(end_marker) + r'(?:\n|$)')
        else:
            # Upgrade the original three-line block, preserving later settings.
            pattern = (r'(?m)^' + re.escape(marker)
                       + r'\nexport CODEX_HOME=[^\n]*\ncodex\(\) \{[^\n]*\}(?:\n|$)')
        updated, count = re.subn(pattern, lambda _: block, text, count=1)
        if count != 1:
            raise ValueError(f'Cannot safely update the CC-Connect shell block in {rcfile}')
    if updated == text:
        return False
    backup = rcfile.with_suffix('.before-cc-connect')
    if not backup.exists():
        shutil.copy2(rcfile, backup)
    rcfile.write_text(updated)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--rcfile', type=Path, required=True, help='Existing Bash startup file')
    parser.add_argument('--codex-home', type=Path, required=True)
    parser.add_argument('--server-url', required=True, help='Existing app-server endpoint')
    parser.add_argument('--name', required=True, help='Registered environment name')
    args = parser.parse_args()
    changed = configure_shell(args.rcfile, args.codex_home, args.server_url, args.name)
    print(('updated: ' if changed else 'unchanged: ') + str(args.rcfile))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError) as exc:
        print('cc-connect shell: ' + str(exc), file=sys.stderr)
        sys.exit(1)
