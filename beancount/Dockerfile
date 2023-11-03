ARG BUILD_FROM
FROM $BUILD_FROM as builder

RUN apk add --update libxml2-dev musl-dev libxslt-dev gcc g++
RUN apk add --update --no-cache python3 py3-pip python3-dev && ln -sf python3 /usr/bin/python
RUN python -m pip install --prefix="/install" fava
RUN cd /tmp && curl -O https://raw.githubusercontent.com/beancount/beancount/master/examples/example.beancount

FROM $BUILD_FROM

RUN apk add --update --no-cache python3 py3-pip nginx 
COPY --from=builder /install /usr/local
COPY --from=builder /tmp/example.beancount /tmp/example.beancount

COPY nginx.conf /etc/nginx/http.d/fava.conf
COPY start.sh /start.sh
RUN chmod u+x /start.sh

ENV FAVA_HOST "0.0.0.0"
ENV PYTHONPATH /usr/local/lib/python3.11/site-packages
CMD /start.sh
# EXPOSE 5000

