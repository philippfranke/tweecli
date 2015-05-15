#!/bin/bash
GOOS=(darwin linux windows)
GOARCH=(386)

RUN="go get github.com/garyburd/go-oauth/oauth;for GOOS in ${GOOS[*]}; do
    for GOARCH in ${GOARCH[*]}; do
      echo Compiling \$GOOS \$GOARCH
      GOOS=\$GOOS GOARCH=\$GOARCH go build -v -o tweecli-v0.1-\$GOOS-\$GOARCH
    done
  done"

docker run --rm -it -v "$PWD":/usr/src/tweecli -w /usr/src/tweecli golang:1.4-cross bash -c "$RUN"
