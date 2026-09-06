import fs from 'node:fs';
import {createRuntime} from './src/runtime/service.mjs';
import {startServer} from './src/transport/router.mjs';

const root = fs.realpathSync(process.env.AGENT_APP_ROOT || '/app');
const port = Number(process.env.PORT || 8888);
const manifest = JSON.parse(fs.readFileSync(new URL('./manifest.json', import.meta.url), 'utf8'));

startServer(createRuntime({root, manifest}), port);
