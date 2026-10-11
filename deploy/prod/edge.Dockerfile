# syntax=docker/dockerfile:1
# Image edge = Caddy + build statis SPA (docs/design/24-docker-compose.md §1.2, §1.4).
# Context: root repo. edge.Dockerfile.dockerignore menggantikan .dockerignore root (yang mengecualikan frontend/).
FROM node:22-bookworm-slim AS web
WORKDIR /web
COPY frontend/package.json frontend/yarn.lock ./
RUN --mount=type=cache,target=/usr/local/share/.cache/yarn yarn install --frozen-lockfile
COPY frontend/ .
# Tautan opsional di landing dan rangkuman demo (kosong = disembunyikan, tidak ada tautan rusak).
ARG VITE_REPO_URL="" VITE_CONTACT_URL="" VITE_VIDEO_URL=""
ENV VITE_REPO_URL=$VITE_REPO_URL VITE_CONTACT_URL=$VITE_CONTACT_URL VITE_VIDEO_URL=$VITE_VIDEO_URL
RUN yarn build

FROM caddy:2.10-alpine
COPY deploy/prod/Caddyfile /etc/caddy/Caddyfile
COPY --from=web /web/dist /srv/ledgerpay
