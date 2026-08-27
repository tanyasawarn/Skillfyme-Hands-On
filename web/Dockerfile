# Web frontend (Next.js) -- PHASE1_MVP_COMPLETION.md §1.1.
#
# NEXT_PUBLIC_API_BASE_URL is inlined at build time (the browser calls
# practice-core directly), so it is a build ARG, not a runtime env var.
# For local compose it must be the URL the *host browser* can reach --
# http://localhost:3001 -- not the compose-internal service name.

# --- deps ---
FROM node:22-slim AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# --- build ---
FROM node:22-slim AS build
WORKDIR /app
ARG NEXT_PUBLIC_API_BASE_URL=http://localhost:3001
ENV NEXT_PUBLIC_API_BASE_URL=${NEXT_PUBLIC_API_BASE_URL}
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

# --- runtime: standalone server bundle only ---
FROM node:22-slim AS runtime
ENV NODE_ENV=production
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends tini \
    && rm -rf /var/lib/apt/lists/*

# next.config.ts sets output: 'standalone' -> .next/standalone contains a
# minimal server + its own trimmed node_modules; static assets are served
# from .next/static and public/.
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public

EXPOSE 3000
USER node
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["node", "server.js"]
