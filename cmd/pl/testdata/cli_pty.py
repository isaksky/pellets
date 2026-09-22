"""Exercise the compiled CLI with real terminal detection and signal delivery."""
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import termios
import time

binary, temporary = sys.argv[1:]
root = Path(temporary)
home = root / 'home'
repo = root / 'demo'
home.mkdir()
repo.mkdir()
env = dict(os.environ, HOME=str(home), USERPROFILE=str(home), NO_COLOR='1', GIT_TERMINAL_PROMPT='0')

def git(cwd, *args):
    return subprocess.check_output(['git', '-C', str(cwd), *args], env=env, stderr=subprocess.PIPE)

def cli(*args, cwd=repo, code=0, input=None):
    result = subprocess.run([binary, '--json', *args], cwd=cwd, env=env, input=input, capture_output=True, text=True, timeout=15)
    assert result.returncode == code, (args, result.returncode, result.stdout, result.stderr)
    return json.loads(result.stdout if code == 0 else result.stderr)

class Terminal:
    def __init__(self, *args, cwd=repo, width=1000):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(cwd)
            os.execve(binary, [binary, *args], env)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', 24, width, 0, 0))
        self.data = b''
        self.status = None
    def read(self):
        if select.select([self.fd], [], [], .05)[0]:
            try:
                self.data += os.read(self.fd, 65536)
            except OSError as e:
                if e.errno != errno.EIO:
                    raise
        if self.status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.status = os.waitstatus_to_exitcode(status)
    def expect(self, text):
        deadline = time.monotonic() + 15
        while text.encode() not in self.data and time.monotonic() < deadline:
            self.read()
            if self.status is not None:
                break
        assert text.encode() in self.data, (text, self.status, self.data.decode(errors='replace'))
        return self
    def send(self, answer):
        os.write(self.fd, answer.encode())
        return self
    def finish(self, code=0):
        deadline = time.monotonic() + 15
        try:
            while self.status is None and time.monotonic() < deadline:
                self.read()
            self.read()
            assert self.status == code, (self.status, code, self.data.decode(errors='replace'))
            return self.data.decode().replace('\r\n', '\n')
        finally:
            self.close()
    def close(self):
        if self.status is None:
            os.kill(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)
            self.status = -9
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None
    def __del__(self):
        if getattr(self, 'fd', None) is not None:
            self.close()

def assert_preview(prompt, *expected):
    # PTYs translate output newlines to CRLF; any remaining CR is record data.
    text = prompt.data.decode().replace('\r\n', '\n')
    assert not any(control in text for control in ('\x1b', '\r', '\b')), repr(text)
    for fragment in expected:
        assert fragment in text, (fragment, repr(text))

# Erasure, cursor movement, carriage return and backspace must stay visible as
# escaped text, never hide a preceding record or overwrite its identity.
controls = '\x1b[2J\x1b[H\rreplaced\b!'
escaped_controls = r'\u001b[2J\u001b[H\u000dreplaced\u0008!'

# First-contact help and errors work before any database is initialized.
for args in [(), ('help',), ('-h',), ('--help',)]:
    text = Terminal(*args).finish()
    assert 'pl list' in text and 'pl help <command>' in text
for args in [('help', 'add'), ('add', '-h'), ('add', '--help')]:
    text = Terminal(*args).finish()
    assert 'Examples:' in text and '- reads stdin' in text
assert 'pl show foo-123' in Terminal('show').finish(2)
assert 'pl --json list' in Terminal('list', '--json').finish(2)
assert 'pl --help' in Terminal('unknown-command').finish(2)
assert json.loads(Terminal('--json').finish(2))['error']['code'] == 'missing_command'
assert not (repo / '.pellets').exists()

git(repo, 'init', '-q')
git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'fixture')
cli('init-db', cwd=root)
cli('project', 'show')

# Group operations finish directly even on a terminal. Explicit stdin remains
# a Markdown payload and is never read as a confirmation or wizard answer.
text = Terminal('group', 'create', 'terminal-context').finish()
assert 'revision=1' in text and '[y/N]' not in text
group = next(g for g in cli('group', 'list')['data'] if g['name'] == 'terminal-context')
group_id = str(group['id'])
payload = 'yes\n# Context\n\n```mermaid\nflowchart LR\n  A --> B\n```\n'
text = Terminal('group', 'edit', group_id, '--context-file', '-').send(payload).send('\x04').finish()
assert '[y/N]' not in text
assert cli('group', 'show', group_id)['data']['context'] == payload
assert payload in Terminal('group', 'show', group_id, width=12).finish()
assert 'group changed; inspect it with group show' in Terminal('group', 'edit', group_id, '--context', 'lost', '--revision', '1').finish(4)
assert 'a group with that exact name already exists' in Terminal('group', 'create', 'terminal-context').finish(4)
assert 'revision=3' in Terminal('group', 'edit', group_id, '--clear-context').finish()
assert json.loads(Terminal('--pretty', 'group', 'show', group_id).finish())['data']['context'] == ''
assert json.loads(Terminal('--json', 'group', 'rename', group_id, 'terminal-renamed').finish())['data']['name'] == 'terminal-renamed'

# Plain installation collects choices and finishes in one invocation.
install = Terminal('skill', 'install').expect('Scope:').send('1\n').expect('Agent:').send('1\n').expect('[y/N]:')
assert str(repo / '.agents/skills/pellets/SKILL.md') in install.data.decode()
text = install.send('yes\n').finish()
assert text.count('[y/N]:') == 1
skill = repo / '.agents/skills/pellets/SKILL.md'
expected_skill = skill.read_text()
assert '--json' in expected_skill
# Replacements need exactly one approval and cancellation preserves bytes.
for answer in ('no\n', '\x04', '\x03'):
    skill.write_text('keep existing skill')
    prompt = Terminal('skill', 'install').expect('Scope:').send('1\n').expect('Agent:').send('1\n').expect('[y/N]:')
    assert 'replace every differing existing skill file' in prompt.data.decode()
    assert 'cancelled' in prompt.send(answer).finish()
    assert skill.read_text() == 'keep existing skill'
prompt = Terminal('skill', 'install').expect('Scope:').send('1\n').expect('Agent:').send('1\n').expect('[y/N]:')
assert prompt.send('yes\n').finish().count('[y/N]:') == 1
assert skill.read_text() == expected_skill
# Revalidation preserves a file changed during confirmation.
skill.write_text('before prompt')
prompt = Terminal('skill', 'install', '--scope', 'repo', '--agent', 'codex').expect('[y/N]:')
skill.write_text('changed while prompting')
assert 'skill destinations changed during confirmation; inspect them and retry' in prompt.send('yes\n').finish(4)
assert skill.read_text() == 'changed while prompting'
# Explicit machine mode never asks, even on a controlling terminal.
assert 'missing_skill_choices' in Terminal('--json', 'skill', 'install').finish(2)
assert 'confirmation_required' in Terminal('--json', 'purge', '--project', 'demo').finish(6)
assert 'missing_skill_choices' in Terminal('--pretty', 'skill', 'install').finish(2)

pellet = cli('add', 'long title ' * 25, '--description-file', '-', input='yes\ncomplete description')['data']
plain = subprocess.run([binary, 'show', pellet['id']], cwd=repo, env=env, capture_output=True, text=True, timeout=15)
assert plain.returncode == 0 and 'yes\ncomplete description' in plain.stdout and '\x1b' not in plain.stdout
narrow = Terminal('list', width=32).finish()
assert all(len(line) <= 32 for line in narrow.splitlines()), narrow
assert 'long title' in narrow and '\x1b' not in narrow
assert json.loads(Terminal('--json', 'show', pellet['id']).finish())['data']['id'] == pellet['id']

# Every consequential action handles decline, terminal EOF and SIGINT cleanly.
cli('close', pellet['id'])
unsafe_title = 'later record ' + controls
unsafe = cli('add', unsafe_title)['data']
cli('close', unsafe['id'])
memory_text = 'knowledge to retain\n' + controls + '\nlast line\tend'
memory = cli('memory', 'add', '--text', memory_text)['data']
assert unsafe['title'] == unsafe_title and memory['text'] == memory_text
purge_rows = [f"  {pellet['id']}  {pellet['title']}\n", f"  {unsafe['id']}  later record {escaped_controls}\n"]
memory_preview = f"Remove memory {memory['id']} from demo (agent):\nknowledge to retain\n{escaped_controls}\nlast line\tend\n"
for action, preview in [
    (('purge', '--project', 'demo'), purge_rows),
    (('memory', 'remove', str(memory['id'])), [memory_preview]),
]:
    for answer in ('no\n', '\x04', '\x03'):
        prompt = Terminal(*action).expect('[y/N]:')
        assert_preview(prompt, *preview)
        assert 'Cancelled' in prompt.send(answer).finish()
        assert cli('show', pellet['id'])['data']['title'] == pellet['title']
        assert cli('show', unsafe['id'])['data']['title'] == unsafe_title
        assert cli('memory', 'show', str(memory['id']))['data']['text'] == memory_text
# Memory changes during confirmation are not silently deleted.
prompt = Terminal('memory', 'remove', str(memory['id'])).expect('[y/N]:')
assert_preview(prompt, memory_preview)
cli('memory', 'approve', str(memory['id']))
assert 'records changed during confirmation; inspect the current state and retry' in prompt.send('yes\n').finish(4)
assert cli('memory', 'show', str(memory['id']))['data']['text'] == memory_text
prompt = Terminal('memory', 'remove', str(memory['id'])).expect('[y/N]:')
assert_preview(prompt, memory_preview)
assert 'Removed memory' in prompt.send('yes\n').finish()
cli('memory', 'show', str(memory['id']), code=3)
# New eligible records invalidate the whole purge, not just the added record.
prompt = Terminal('purge', '--project', 'demo').expect('[y/N]:')
assert_preview(prompt, *purge_rows)
other = cli('add', 'closed during prompt')['data']
cli('close', other['id'])
assert 'records changed during confirmation; inspect the current state and retry' in prompt.send('yes\n').finish(4)
cli('show', pellet['id'])
assert cli('show', unsafe['id'])['data']['title'] == unsafe_title
cli('show', other['id'])
prompt = Terminal('purge', '--project', 'demo').expect('[y/N]:')
assert_preview(prompt, *purge_rows, f"  {other['id']}  {other['title']}\n")
assert 'Purged 3 closed pellets' in prompt.send('yes\n').finish()
for record in (pellet, unsafe, other):
    cli('show', record['id'], code=3)

# Explicit cross-workspace recovery displays and revalidates the stored owner.
# Put controls in both the worktree root and Git directory without changing the
# project code, which is derived from the final directory component.
recovery_repo = root / ('workspace ' + controls) / 'recovery'
recovery_repo.mkdir(parents=True)
git(recovery_repo, 'init', '-q')
git(recovery_repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'fixture')
recovery_project = cli('project', 'show', cwd=recovery_repo)['data']
linked = root / 'linked'
git(recovery_repo, 'worktree', 'add', '-q', '--detach', str(linked), 'HEAD')
cli('project', 'show', cwd=linked)
owned_title = 'owned work ' + controls
owned = cli('add', owned_title, cwd=recovery_repo)['data']
cli('start', owned['id'], cwd=recovery_repo)
owner = recovery_project['workspaces'][0]
workspace = str(owner['id'])
def escaped_workspace_path(field):
    # Use the registered path: discovery resolves symlinks and filesystem case.
    path = Path(owner[field])
    if owner[field + '_relative']:
        path = root.resolve() / path
    text = str(path)
    assert all(control in text for control in ('\x1b', '\r', '\b')), repr(text)
    return text.replace('\x1b', r'\u001b').replace('\r', r'\u000d').replace('\b', r'\u0008')
escaped_root = escaped_workspace_path('root_path')
escaped_git_dir = escaped_workspace_path('git_dir')
def recovery_preview(title):
    return f"release {owned['id']} ({title}), recovering recorded workspace {workspace} at {escaped_root} (Git directory {escaped_git_dir}).\n"
for answer in ('no\n', '\x04', '\x03'):
    prompt = Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:')
    assert_preview(prompt, recovery_preview('owned work ' + escaped_controls))
    assert 'Cancelled' in prompt.send(answer).finish()
    unchanged = cli('show', owned['id'], cwd=recovery_repo)['data']
    assert unchanged['status'] == 'in_progress' and unchanged['title'] == owned_title
    assert unchanged['workspace']['root_path'] == owner['root_path']
    assert unchanged['workspace']['git_dir'] == owner['git_dir']
prompt = Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:')
assert_preview(prompt, recovery_preview('owned work ' + escaped_controls))
changed_title = 'changed owner record ' + controls
cli('edit', owned['id'], '--title', changed_title, cwd=recovery_repo)
assert 'records changed during confirmation; inspect the current state and retry' in prompt.send('yes\n').finish(4)
prompt = Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:')
assert_preview(prompt, recovery_preview('changed owner record ' + escaped_controls))
assert 'Recovered workspace' in prompt.send('yes\n').finish()
recovered = cli('show', owned['id'], cwd=recovery_repo)['data']
assert recovered['status'] == 'open' and recovered['title'] == changed_title and recovered['workspace'] is None

# Redirect-conflict rename performs the originally requested command after yes.
foreign = root / 'foreign'
foreign.mkdir()
git(foreign, 'init', '-q')
cli('project', 'show', cwd=foreign)
cli('project', 'rename', 'target', cwd=foreign)
for answer in ('no\n', '\x04', '\x03'):
    prompt = Terminal('project', 'rename', 'foreign').expect('[y/N]:')
    assert 'foreign -> target' in prompt.data.decode()
    assert 'cancelled' in prompt.send(answer).finish()
    assert cli('project', 'show')['data']['code'] == 'demo'
assert 'Renamed project demo -> foreign' in Terminal('project', 'rename', 'foreign').expect('[y/N]:').send('yes\n').finish()

# A non-TTY with an open input pipe exits without reading even one answer.
for args in [('memory', 'remove', '1'), ('purge', '--project', 'foreign'), ('skill', 'install')]:
    process = subprocess.Popen([binary, *args], cwd=repo, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        process.wait(timeout=10)
        assert process.returncode in (2, 6)
        error = process.stderr.read().decode()
        assert 'pl --json' in error and 'Error:' in error, error
    finally:
        if process.poll() is None:
            process.kill()
        process.communicate()
print('PTY checks passed: defaults, machine mode, width, payloads, all confirmations, cancellation, and stale approvals')
