# ---------- build ----------
FROM golang:1.27-alpine AS build

WORKDIR /src
RUN apk add --no-cache git ca-certificates tzdata

# go.mod/go.sum dikomit sehingga build dapat direproduksi tanpa go mod tidy.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- runtime ----------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app
ENV TZ=Asia/Jakarta

WORKDIR /app
COPY --from=build /out/server /app/server
USER app

EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["--mode=api"]
