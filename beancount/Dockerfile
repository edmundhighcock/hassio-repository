ARG BUILD_FROM
FROM $BUILD_FROM as builder

RUN apk add --update libxml2-dev libxslt-dev gcc musl-dev g++
RUN apk add --update --no-cache python3 py3-pip python3-dev && ln -sf python3 /usr/bin/python
RUN python -m pip install --prefix="/install" fava

FROM $BUILD_FROM

COPY --from=builder /install /usr/local

ENV FAVA_HOST "0.0.0.0"
EXPOSE 5000
CMD /usr/local/install/bin/fava

