FROM node:18-alpine AS builder
WORKDIR /build
COPY package.json package-lock.json* yarn.lock* pnpm-lock.yaml* ./
RUN npm install
COPY . .
RUN npm run build

FROM nginx:alpine
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=builder /build/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
