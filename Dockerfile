FROM golang:latest
WORKDIR /opt/galah
RUN <<EOF
apt update
apt install openssl -y
EOF
COPY . .
RUN <<EOF
go mod download
go build -o galah ./cmd/galah
EOF
ENTRYPOINT ["./entrypoint.sh"]
