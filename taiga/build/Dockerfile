#ARG BUILD_FROM
#FROM $BUILD_FROM
#ARG BUILD_ARCH
#FROM $(BUILD_ARCH)/ubuntu:20.04
FROM ubuntu:20.04 AS build
#FROM homeassistant/amd64-base-ubuntu:20.04

RUN apt-get update
RUN DEBIAN_FRONTEND=noninteractive TZ=Etc/UTC apt-get -y install tzdata.
RUN apt-get install -y build-essential binutils-doc autoconf flex bison libjpeg-dev
RUN apt-get install -y libfreetype6-dev zlib1g-dev libzmq3-dev libgdbm-dev libncurses5-dev
RUN apt-get install -y automake libtool curl git tmux gettext
RUN apt-get install -y nginx
# RUN apt-get install -y rabbitmq-server
#
#
RUN apt-get install -y python3 python3-pip python3-dev python3-venv
RUN apt-get install -y libxml2-dev libxslt-dev
RUN apt-get install -y libssl-dev libffi-dev

RUN apt-get install -y sudo
RUN curl -sL https://deb.nodesource.com/setup_12.x | bash -
RUN apt-get install -y nodejs

RUN adduser taiga
RUN adduser taiga sudo

USER taiga

RUN cd ~ && \
     git clone https://github.com/kaleidos-ventures/taiga-back.git taiga-back && \
     cd taiga-back && \
     git checkout stable

WORKDIR /home/taiga/taiga-back

RUN bash -c "python3 -m venv .venv --prompt taiga-back && source .venv/bin/activate && pip install --upgrade pip wheel"

USER root

RUN apt-get install -y postgresql-12 postgresql-contrib-12 postgresql-doc-12 postgresql-server-dev-12

USER taiga

RUN bash -c "source .venv/bin/activate && pip install -r requirements.txt && pip install git+https://github.com/kaleidos-ventures/taiga-contrib-protected.git@stable#egg=taiga-contrib-protected"

RUN cp settings/config.py.prod.example settings/config.py

RUN cd ~ && \
   git clone https://github.com/kaleidos-ventures/taiga-front-dist.git taiga-front-dist && \
   cd taiga-front-dist && \
   git checkout stable

RUN cd ~ && \
  git clone https://github.com/kaleidos-ventures/taiga-events.git taiga-events && \
  cd taiga-events && \
  git checkout stable

RUN cd ~/taiga-events && npm install

COPY events.env /home/taiga/taiga-events/.env 

RUN cd ~ && \
  git clone https://github.com/kaleidos-ventures/taiga-protected.git taiga-protected && \
  cd taiga-protected && \
  git checkout stable

RUN cd ~/taiga-protected && \
  python3 -m venv .venv --prompt taiga-protected && \
  bash -c "source .venv/bin/activate &&  pip install --upgrade pip wheel && pip install -r requirements.txt" 

COPY protected.env /home/taiga/taiga-protected/.env 

COPY backend_config.py /home/taiga/taiga-back/settings/config.py
COPY frontend-conf.json /home/taiga/taiga-front-dist/dist/conf.json

USER root


#COPY migrate.sh /migrate.sh
#COPY start.sh /start.sh

#RUN chown taiga /migrate.sh && chown taiga /start.sh

RUN apt-get -y autoremove && apt-get -y clean && apt-get -y autoclean

RUN rm /etc/nginx/sites-enabled/default

COPY nginx.conf /etc/nginx/conf.d/taiga.conf


#FROM scratch
#COPY --from=build / /

USER taiga

RUN mkdir -p ~/logs

CMD bash /start.sh




#ENV TAIGA_SECRET_KEY=tobesorted

USER root
RUN apt-get install -y jq


## Copy data for add-on
#COPY run.sh /
#RUN chmod a+x /run.sh
#
#CMD [ "/run.sh" ]


# pg_ctlcluster 12 main start

