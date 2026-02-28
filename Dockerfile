# ---------- build stage ----------
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache build-base

WORKDIR /app

# cache deps
COPY go.mod go.sum ./
RUN go mod tidy && go mod download

COPY . .

# sqlite precisa CGO
ENV CGO_ENABLED=1
RUN go build -o finance-manager main.go


# ---------- runtime stage ----------
FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /data

COPY --from=builder /app/finance-manager /usr/local/bin/finance-manager

# pasta padrão onde você vai montar OFX + planilha + db
VOLUME ["/data"]

ENTRYPOINT ["finance-manager"]
