# syntax=docker/dockerfile:1
FROM golang:1.23 AS base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o bonjack-tools-backend-go


FROM gcr.io/distroless/static-debian11

COPY --from=base /app/bonjack-tools-backend-go .

EXPOSE 8080

ENV PORT=8080

CMD ["bonjack-tools-backend-go"]
