FROM ghcr.io/umputun/baseimage/buildgo:latest AS build

ARG GIT_BRANCH
ARG GITHUB_SHA
ARG CI

ADD . /build
WORKDIR /build

RUN go version

RUN \
 if [ -z "$CI" ] ; then \
 echo "runs outside of CI" && version=$(git rev-parse --abbrev-ref HEAD)-$(git log -1 --format=%h)-$(date +%Y%m%dT%H:%M:%S); \
 else version=${GIT_BRANCH}-${GITHUB_SHA:0:7}-$(date +%Y%m%dT%H:%M:%S); fi && \
 echo "version=$version" && \
 go build -o /build/weblist -ldflags "-X main.revision=${version} -s -w"


FROM ghcr.io/umputun/baseimage/scratch:latest
LABEL org.opencontainers.image.source="https://github.com/umputun/weblist"

COPY --from=build /build/weblist /srv/weblist
VOLUME ["/data"]
WORKDIR /data

USER app

# Expose the port the app will run on
EXPOSE 8080

# Run the application
ENTRYPOINT ["/srv/weblist"]
CMD ["--listen=:8080", "--theme=dark"]
