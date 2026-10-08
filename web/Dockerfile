# Build kedua SPA lalu sajikan lewat Caddy.
#
# Konteks build adalah akar repo (lihat docker-compose.yml) supaya Dockerfile
# ini bisa menyalin monorepo web/ sekaligus infra/caddy/.

# ---------- build ----------
FROM node:22-alpine AS build

WORKDIR /src
COPY web/package.json web/package-lock.json ./
COPY web/tsconfig.base.json ./
COPY web/packages ./packages
COPY web/apps ./apps

RUN npm ci --no-audit --no-fund
RUN npm run build

# ---------- serve ----------
FROM caddy:2-alpine

COPY --from=build /src/apps/customer/dist /srv/customer
COPY --from=build /src/apps/admin/dist /srv/admin
COPY infra/caddy/Caddyfile.dev /etc/caddy/Caddyfile

EXPOSE 80 81
