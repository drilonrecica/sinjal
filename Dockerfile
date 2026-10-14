# Sinjal: one static binary in an empty image (decision P0-03, docs/16).
# No shell, no package manager, nothing to patch; the binary checks its
# own health. Build: docker build -t sinjal .

ARG GO_VERSION=1.27

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# cmd/sinjal imports time/tzdata, so SINJAL_TIMEZONE works without zoneinfo.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/sinjal ./cmd/sinjal \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/sinjal /sinjal
# A new named volume takes this directory's owner, so the non-root user can
# write to it.
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
ENV SINJAL_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/sinjal", "healthcheck"]

ENTRYPOINT ["/sinjal"]
CMD ["serve"]
