# Build from the REPO ROOT (the dashboard imports ../data/*.seed.json):
#   docker build -f dashboard/Dockerfile -t llmgate/dashboard:dev .
FROM node:22-alpine AS build
WORKDIR /app/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard/ ./
COPY data/ /app/data/
RUN npm run build

FROM nginx:1.27-alpine
# The official image runs envsubst over /etc/nginx/templates/*.template at start-up.
COPY dashboard/nginx.conf.template /etc/nginx/templates/default.conf.template
# React dashboard at /dashboard/, guide page (docs/index.html + its two images) at /
COPY --from=build /app/dashboard/dist /usr/share/nginx/html/dashboard
COPY docs/index.html docs/request-flow.png docs/architecture.png /usr/share/nginx/html/
