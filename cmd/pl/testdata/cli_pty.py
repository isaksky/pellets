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

git(repo, 'init', '-q')
git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-qm', 'fixture')
cli('init-db', cwd=root)
project = cli('project', 'show')['data']

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
assert 'skill_plan_changed' in prompt.send('yes\n').finish(4)
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
memory = cli('memory', 'add', '--text', 'knowledge to retain')['data']
for action, check in [
    (('purge', '--project', 'demo'), lambda: cli('show', pellet['id'])),
    (('memory', 'remove', str(memory['id'])), lambda: cli('memory', 'show', str(memory['id']))),
]:
    for answer in ('no\n', '\x04', '\x03'):
        prompt = Terminal(*action).expect('[y/N]:')
        assert 'Cancelled' in prompt.send(answer).finish()
        check()
# Memory changes during confirmation are not silently deleted.
prompt = Terminal('memory', 'remove', str(memory['id'])).expect('[y/N]:')
cli('memory', 'approve', str(memory['id']))
assert 'confirmation_changed' in prompt.send('yes\n').finish(4)
cli('memory', 'show', str(memory['id']))
assert 'Removed memory' in Terminal('memory', 'remove', str(memory['id'])).expect('[y/N]:').send('yes\n').finish()
cli('memory', 'show', str(memory['id']), code=3)
# New eligible records invalidate the whole purge, not just the added record.
prompt = Terminal('purge', '--project', 'demo').expect('[y/N]:')
other = cli('add', 'closed during prompt')['data']
cli('close', other['id'])
assert 'confirmation_changed' in prompt.send('yes\n').finish(4)
cli('show', pellet['id'])
cli('show', other['id'])
assert 'Purged 2 closed pellets' in Terminal('purge', '--project', 'demo').expect('[y/N]:').send('yes\n').finish()

# Explicit cross-workspace recovery displays and revalidates the stored owner.
linked = root / 'linked'
git(repo, 'worktree', 'add', '-q', '--detach', str(linked), 'HEAD')
cli('project', 'show', cwd=linked)
owned = cli('add', 'owned work')['data']
cli('start', owned['id'])
workspace = str(project['workspaces'][0]['id'])
for answer in ('no\n', '\x04', '\x03'):
    prompt = Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:')
    assert str(repo) in prompt.data.decode()
    assert 'Cancelled' in prompt.send(answer).finish()
    assert cli('show', owned['id'])['data']['status'] == 'in_progress'
prompt = Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:')
cli('edit', owned['id'], '--title', 'changed owner record')
assert 'confirmation_changed' in prompt.send('yes\n').finish(4)
assert 'Recovered workspace' in Terminal('release', owned['id'], '--recover-workspace', workspace, cwd=linked).expect('[y/N]:').send('yes\n').finish()

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
