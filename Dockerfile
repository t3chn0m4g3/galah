FROM golang:latest
WORKDIR /galah
RUN <<EOF
apt update
apt install jq -y
EOF
COPY . .
RUN <<EOF
go mod download
go build -o galah ./cmd/galah
EOF
ENTRYPOINT ["./galah"]
