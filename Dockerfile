# syntax=docker/dockerfile:1
#
# Built by GoReleaser dockers_v2: the linux/amd64 and linux/arm64 binaries are
# laid out per platform in the build context as $TARGETPLATFORM/dvstrip.
FROM alpine:latest

# A recent jellyfin-ffmpeg build with dovi_rpu=strip=1 support ships in Alpine
# community; its binaries live under /usr/lib/jellyfin-ffmpeg/. dovi-tool and
# hdr10plus-tool are only packaged in edge/community, so add that repo and pull
# just those two from it. Everything is native musl — no libc mismatch.
RUN set -eux; \
    echo "https://dl-cdn.alpinelinux.org/alpine/edge/community" >> /etc/apk/repositories; \
    apk add --no-cache jellyfin-ffmpeg dovi-tool hdr10plus-tool mkvtoolnix; \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffmpeg  /usr/local/bin/ffmpeg; \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/dvstrip /usr/local/bin/dvstrip

ENTRYPOINT ["dvstrip"]
