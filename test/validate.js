#!/usr/bin/env node
// Validate manifests against the Agent Plugins 1.0.0 JSON Schemas (fetched live) using ajv via npx.
'use strict';
const { execFileSync } = require('node:child_process');
execFileSync('sh', ['-c', `
set -e
d=$(mktemp -d)
curl -fsS https://agent-plugins.org/schemas/1.0.0/plugin.schema.json -o $d/plugin.schema.json
curl -fsS https://agent-plugins.org/schemas/1.0.0/mcp.schema.json -o $d/mcp.schema.json
npx -y -p ajv-cli@5 -p ajv-formats ajv validate --spec=draft2020 --strict=false -s $d/plugin.schema.json -d plugin.json
npx -y -p ajv-cli@5 -p ajv-formats ajv validate --spec=draft2020 --strict=false -s $d/mcp.schema.json -d mcp.json
rm -rf $d
`], { stdio: 'inherit', cwd: require('node:path').join(__dirname, '..') });
