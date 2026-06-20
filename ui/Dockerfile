FROM node:22-alpine AS base

WORKDIR /app

# Install bun for faster installs (optional)
RUN npm install -g bun

COPY ui/package.json ui/package-lock.json* ./
RUN bun install --frozen-lockfile || npm install

COPY ui/ ./

RUN bun run build || npm run build

# Production image
FROM node:22-alpine AS runner
WORKDIR /app

ENV NODE_ENV=production

COPY --from=base /app/.next/standalone ./
COPY --from=base /app/.next/static ./.next/static
COPY --from=base /app/public ./public

EXPOSE 3000

CMD ["node", "server.js"]
