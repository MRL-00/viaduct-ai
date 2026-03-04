FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/viaduct ./cmd/viaduct

FROM scratch
COPY --from=builder /out/viaduct /viaduct
ENTRYPOINT ["/viaduct"]
