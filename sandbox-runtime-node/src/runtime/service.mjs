import fs from 'node:fs';
import path from 'node:path';
import {ProcessManager} from './process-manager.mjs';
import {FileService} from './files.mjs';

export function createRuntime({root, manifest}) {
  const processManager = new ProcessManager({
    root,
    entrypoint: path.join(root, 'execute.mjs'),
  });
  const files = new FileService(root);
  return {
    manifest,
    health: () => ({status: 'ok', version: 'node-runtime/1'}),
    execute: (id) => processManager.execute(id),
    cancel: (id) => processManager.cancel(id),
    files,
    readExecutionId: () => {
      try {
        return JSON.parse(fs.readFileSync(path.join(root, 'run.json'), 'utf8')).execution_id;
      } catch {
        return null;
      }
    },
    validCommand: (command) => {
      if (
        typeof command !== 'string' ||
        command.length === 0 ||
        command.length > 256 ||
        /[;&|`$<>\\\n\r]/.test(command)
      )
        return false;
      const entry = path.join(root, 'execute.mjs');
      return (
        command === 'execute.mjs' ||
        command === `${path.basename(process.execPath)} execute.mjs` ||
        command === 'node execute.mjs' ||
        command === `node ${entry}` ||
        command === `${process.execPath} ${entry}` ||
        command === `node ${entry} --config ${path.join(root, 'run.json')}` ||
        command === `${process.execPath} ${entry} --config ${path.join(root, 'run.json')}`
      );
    },
  };
}
