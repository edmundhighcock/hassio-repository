FROM rabbitmq:3.11.7-alpine

ENV RABBITMQ_DEFAULT_USER=taiga
# ENV RABBITMQ_DEFAULT_PASS=taiga
ENV RABBITMQ_DEFAULT_VHOST=taiga

COPY start.sh /start.sh

RUN apk add jq

CMD bash /start.sh

