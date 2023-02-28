FROM alpine:3.17

ARG TARGETOS
ARG TARGETARCH

LABEL maintainer="Daniel Ting <danielting@aliyun.com>" \
  org.label-schema.name="Drone SCP Plugin" \
  org.label-schema.vendor="Daniel Ting" \
  org.label-schema.schema-version="1.0"

RUN apk add --no-cache ca-certificates && \
  rm -rf /var/cache/apk/*

COPY release/${TARGETOS}/${TARGETARCH}/drone-scp /bin/

ENTRYPOINT ["/bin/drone-scp"]
