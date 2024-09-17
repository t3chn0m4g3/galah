#!/usr/bin/env bash
if ! [ -f "config/cert/key.pem" ]; then
	openssl req \
	      -nodes \
	      -x509 \
	      -newkey rsa:2048 \
	      -keyout "config/cert/key.pem" \
	      -out "config/cert/cert.pem" \
	      -days 3650 \
	      -subj '/C=AU/ST=Some-State/O=Internet Widgits Pty Ltd'
fi
exec ./galah
