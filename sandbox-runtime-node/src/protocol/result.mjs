import fs from 'node:fs';
import path from 'node:path';

export function writeResult(root, value) {
  const output = path.join(root, 'output');
  const resultPath = path.join(output, 'result.json');
  const temporaryPath = `${resultPath}.tmp-${process.pid}`;
  fs.mkdirSync(output, {recursive: true});
  fs.writeFileSync(temporaryPath, JSON.stringify(value));
  fs.renameSync(temporaryPath, resultPath);
}
