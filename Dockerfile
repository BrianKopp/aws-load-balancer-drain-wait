FROM golang:1.17-alpine as build

ENV GO111MODULE=on
ENV CGO_ENABLED=1
ENV GOOS=linux

WORKDIR /app

# Download dependencies
COPY go.mod .
COPY go.sum .
RUN go mod download

# Copy go code and build
COPY main.go .
RUN go build -o aws-load-balancer-drain-wait
RUN ls -ahl

# Deploy stage
FROM alpine
WORKDIR /usr/local/bin
COPY --from=build /app/aws-load-balancer-drain-wait /usr/local/bin/aws-load-balancer-drain-wait
RUN ls -ahl
RUN cat /etc/group
USER nobody
CMD ["aws-load-balancer-drain-wait"]
