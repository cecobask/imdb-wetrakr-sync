FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X github.com/cecobask/imdb-wetrakr-sync/cmd.Version=${VERSION}" -o /out/iws .

# Minimal headless Chromium maintained by the chromedp project.
FROM chromedp/headless-shell:156.0.8078.48
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/iws /usr/local/bin/iws
ENV IWS_IMDB_BROWSERPATH=/headless-shell/headless-shell \
    IWS_WETRAKR_TOKENFILE=/data/wetrakr-token.json
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["iws"]
CMD ["sync"]
