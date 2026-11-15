# Development image (Vite dev server). Requires package-lock.json: run `make frontend-deps` first.
ARG NODE_VERSION=22
FROM node:${NODE_VERSION}-alpine
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
