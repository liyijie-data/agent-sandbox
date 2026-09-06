import {spawn} from 'node:child_process';

const MAX_OUTPUT = 2 * 1024 * 1024;

function collect(stream) {
  return new Promise((resolve) => {
    const chunks = [];
    let size = 0;
    stream.on('data', (chunk) => {
      if (size >= MAX_OUTPUT) return;
      const part = chunk.subarray(0, MAX_OUTPUT - size);
      chunks.push(part);
      size += part.length;
    });
    stream.on('end', () => resolve(Buffer.concat(chunks)));
    stream.on('error', () => resolve(Buffer.concat(chunks)));
  });
}

export class ProcessManager {
  constructor({root, entrypoint}) {
    this.root = root;
    this.entrypoint = entrypoint;
    this.active = null;
  }

  execute(id) {
    if (this.active) {
      return Promise.reject(Object.assign(new Error('busy'), {code: 'runtime_state_conflict'}));
    }
    const proc = spawn(process.execPath, [this.entrypoint, '--config', `${this.root}/run.json`], {
      cwd: this.root,
      shell: false,
      detached: true,
    });
    const state = {id, proc, timer: null, cancelling: false};
    this.active = state;
    const stdout = collect(proc.stdout);
    const stderr = collect(proc.stderr);
    return new Promise((resolve) => {
      proc.once('error', () => {
        this.finish(state);
        resolve({error: true});
      });
      proc.once('close', async (code) => {
        const [out, err] = await Promise.all([stdout, stderr]);
        this.finish(state);
        resolve({
          execution_id: id,
          stdout: out.toString('utf8'),
          stderr: err.toString('utf8'),
          exit_code: code ?? -1,
        });
      });
    });
  }

  cancel(id) {
    if (!this.active || this.active.id !== id) return {status: 'stopped', execution_id: id};
    const state = this.active;
    if (!state.cancelling) {
      state.cancelling = true;
      try {
        process.kill(-state.proc.pid, 'SIGTERM');
      } catch {}
      state.timer = setTimeout(() => {
        try {
          process.kill(-state.proc.pid, 'SIGKILL');
        } catch {}
      }, 10_000);
    }
    return {status: 'accepted', execution_id: id};
  }

  finish(state) {
    if (this.active !== state) return;
    clearTimeout(state.timer);
    this.active = null;
  }
}
