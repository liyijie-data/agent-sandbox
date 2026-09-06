import {executeRun} from './src/agent/runner.mjs';

const root = process.env.AGENT_APP_ROOT || '/app';
const succeeded = await executeRun(root);
process.exitCode = succeeded ? 0 : 1;
