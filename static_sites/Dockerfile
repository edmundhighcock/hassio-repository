ARG BUILD_FROM
FROM ${BUILD_FROM}

RUN apk add --no-cache nginx

# nginx config, front page, placeholder, and the vendored chart library
# (ECharts 6.1.0, Apache-2.0, sha512-verified against the npm registry).
COPY rootfs/ /
RUN chmod a+x /run.sh

CMD ["/run.sh"]
