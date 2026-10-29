# syntax=docker/dockerfile:1
# Context: sdn-agent/docker. Source versions are independent of VPP and FRR.
ARG ALPINE_VERSION=3.22
FROM alpine:${ALPINE_VERSION} AS build
ARG SNORT_REF=3.12.2.0
ARG DAQ_REF=v3.0.27
ARG BUILD_JOBS=12
RUN apk add --no-cache build-base cmake ninja curl git autoconf automake libtool \
    linux-headers flex bison libpcap-dev libdnet-dev hwloc-dev luajit-dev \
    openssl-dev pcre2-dev zlib-dev libunwind-dev xz-dev libuuid ccache
RUN mkdir -p /src/daq /src/snort /out/usr/share/sdn-snort && \
    curl -fsSL --retry 3 "https://codeload.github.com/snort3/libdaq/tar.gz/refs/tags/${DAQ_REF}" -o /tmp/daq.tar.gz && \
    curl -fsSL --retry 3 "https://codeload.github.com/snort3/snort3/tar.gz/refs/tags/${SNORT_REF}" -o /tmp/snort.tar.gz && \
    sha256sum /tmp/daq.tar.gz /tmp/snort.tar.gz > /out/usr/share/sdn-snort/source.sha256 && \
    tar -xzf /tmp/daq.tar.gz -C /src/daq --strip-components=1 && \
    tar -xzf /tmp/snort.tar.gz -C /src/snort --strip-components=1 && \
    printf '%s\n' "${SNORT_REF}" > /out/usr/share/sdn-snort/version && \
    cd /src/daq && ./bootstrap && ./configure --prefix=/usr && \
    make -j "${BUILD_JOBS}" && make install && make DESTDIR=/out install
RUN apk add --no-cache flex-dev libtirpc-dev
RUN --mount=type=cache,target=/root/.cache/ccache \
    cmake -S /src/snort -B /build -G Ninja -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
      -DCMAKE_C_COMPILER_LAUNCHER=ccache -DCMAKE_CXX_COMPILER_LAUNCHER=ccache \
      -DENABLE_SHELL=ON && \
    cmake --build /build --parallel "${BUILD_JOBS}" && DESTDIR=/out cmake --install /build --strip && \
    rm -rf /out/usr/include /out/usr/lib/pkgconfig && find /out -name '*.a' -delete

FROM golang:1.25-alpine3.22 AS confd
ARG CONFD_REF=919444eb6cf721d198b2bb18581d0f0b3734d107
RUN apk add --no-cache git
RUN git clone https://github.com/kelseyhightower/confd.git /src && cd /src && git checkout "${CONFD_REF}"
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/confd .

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache libpcap libdnet hwloc luajit openssl pcre2 zlib libunwind xz-libs libstdc++ libgcc libtirpc libuuid
COPY --from=build /out/ /
COPY --from=confd /out/confd /usr/local/bin/confd
RUN snort -V && confd -version
