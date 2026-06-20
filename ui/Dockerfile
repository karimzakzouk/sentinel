FROM node:22-alpine AS base

WORKDIR /app

# Install bun for faster installs
RUN npm install -g bun

# Copy package files and install
COPY ui/package.json ui/bun.lock* ui/package-lock.json* ./
RUN bun install --frozen-lockfile || bun install

# Copy the rest of the UI source
COPY ui/ ./

# Build the standalone Next.js app
RUN bun run build

# Make sure /app/public exists in the base image (Next.js standalone build
# may not include it if the project never had one — we created an empty one
# in the repo, but be defensive)
RUN mkdir -p /app/public

# Production image — minimal
FROM node:22-alpine AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV PORT=3000

# Copy the standalone server (Next.js output:standalone produces /app/.next/standalone)
COPY --from=base /app/.next/standalone ./
COPY --from=base /app/.next/static ./.next/static
COPY --from=base /app/public ./public

EXPOSE 3000

CMD ["node", "server.js"]
