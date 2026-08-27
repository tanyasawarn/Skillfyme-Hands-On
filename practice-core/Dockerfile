# Practice Core (NestJS) -- PHASE1_MVP_COMPLETION.md §1.1.
#
# Layout note: at runtime the app resolves contracts/*.proto and
# content/* via `path.resolve(process.cwd(), '..', <subpath>)` (see
# src/common/repo-relative-path.ts's cwd fallback). So the image keeps
# the app at /app/practice-core with /app/contracts and /app/content one
# level up, and the container runs with WORKDIR /app/practice-core.
#
# Build context is practice-core/ (docker-compose.yml build.context);
# contracts/ and content/ are copied in by the compose build via an
# additional_context, or bind-mounted at runtime -- see the compose file.

# --- deps stage: full install for the build ---
FROM node:22-slim AS deps
WORKDIR /app/practice-core
COPY package.json package-lock.json ./
RUN npm ci

# --- build stage ---
FROM node:22-slim AS build
WORKDIR /app/practice-core
COPY --from=deps /app/practice-core/node_modules ./node_modules
COPY . .
RUN npm run build

# --- prod deps: drop devDependencies ---
FROM node:22-slim AS proddeps
WORKDIR /app/practice-core
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

# --- runtime stage ---
FROM node:22-slim AS runtime
ENV NODE_ENV=production
WORKDIR /app/practice-core

# tini for correct signal handling (Nest's SIGTERM shutdown hook).
RUN apt-get update && apt-get install -y --no-install-recommends tini \
    && rm -rf /var/lib/apt/lists/*

COPY --from=proddeps /app/practice-core/node_modules ./node_modules
COPY --from=build /app/practice-core/dist ./dist
COPY package.json ./
# db/migrations kept in the image so an operator can apply them from a
# one-shot `docker compose run` without the source tree.
COPY db ./db

EXPOSE 3001
USER node
ENTRYPOINT ["/usr/bin/tini", "--"]
# Nest compiles to dist/src/main.js (sourceRoot "src"); package.json's
# start:prod is corrected to match.
CMD ["node", "dist/src/main.js"]
