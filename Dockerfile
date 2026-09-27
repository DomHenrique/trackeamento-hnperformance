# ==========================================
# Build Stage (Golang)
# ==========================================
FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata

ENV GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Compilação otimizada dos três serviços (api, ingester e dispatcher)
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/ingester ./cmd/ingester && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/dispatcher ./cmd/dispatcher

# ==========================================
# Final Stage (Alpine Linux Leve)
# ==========================================
FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup

# Copia os binários compilados
COPY --from=builder /app/bin/api /app/bin/api
COPY --from=builder /app/bin/ingester /app/bin/ingester
COPY --from=builder /app/bin/dispatcher /app/bin/dispatcher

# Copia arquivos estáticos do SDK do cliente
COPY sdk/ /app/sdk/

# Script de entrada multifunção
COPY docker-entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

USER 10001:10001

EXPOSE 8080

ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["api"]
