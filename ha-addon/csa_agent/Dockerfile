ARG BUILD_FROM
FROM $BUILD_FROM

ARG BUILD_ARCH
# csa-agent from the project's GitHub release (checksum-verified), and the
# Home Assistant CLI ("ha") that talks to the Supervisor API.
RUN apk add --no-cache bash curl jq ca-certificates coreutils \
 && case "$BUILD_ARCH" in amd64) A=amd64 ;; aarch64) A=arm64 ;; *) echo "unsupported arch $BUILD_ARCH"; exit 1 ;; esac \
 && mkdir -p /tmp/csa && cd /tmp/csa \
 && curl -fsSL -o csa.tgz "https://github.com/cstone1983/Claude-server-agent/releases/latest/download/csa_linux_${A}.tar.gz" \
 && curl -fsSL -o checksums.txt "https://github.com/cstone1983/Claude-server-agent/releases/latest/download/checksums.txt" \
 && grep " csa_linux_${A}.tar.gz\$" checksums.txt | sed "s/csa_linux_${A}.tar.gz/csa.tgz/" | sha256sum -c - \
 && tar -xzf csa.tgz && install -m 0755 csa-agent /usr/local/bin/csa-agent \
 && curl -fsSL -o /usr/local/bin/ha.bin "https://github.com/home-assistant/cli/releases/latest/download/ha_${BUILD_ARCH}" \
 && chmod 0755 /usr/local/bin/ha.bin \
 && cd / && rm -rf /tmp/csa

# The agent starts commands with a clean environment, so "ha" is a wrapper that
# loads the add-on's Supervisor token from s6's container environment.
COPY ha-wrapper.sh /usr/local/bin/ha
COPY run.sh /run.sh
COPY policy.yaml /policy.default.yaml
RUN chmod 0755 /run.sh /usr/local/bin/ha
CMD ["/run.sh"]
