FROM node:22-alpine
WORKDIR /app
COPY package.json package-lock.json manifest.json server.mjs execute.mjs recovery.mjs /app/
COPY src /app/src
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
RUN mkdir -p /app/input /app/output /app/workspace && chown -R node:node /app
USER node
ENTRYPOINT ["node", "/app/server.mjs"]
