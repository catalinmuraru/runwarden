# Example scripts image: how a scripts repository builds on the Runwarden base image.
# Scripts, their tools and runner configs live in this layer; Runwarden updates only change BASE.
#
#   docker build -f examples/Dockerfile --build-arg BASE=ghcr.io/catalinmuraru/runwarden:0.4.0 -t my-scripts .

ARG BASE=ghcr.io/catalinmuraru/runwarden:0.4.0@sha256:9c16b3966c8aabbd79e35e96d7a0c7aeee381a1043ce924ed893238078113107
FROM ${BASE}

# Install the tools your scripts need here, e.g.:
#   USER root
#   RUN apt-get update && apt-get install -y --no-install-recommends curl jq && rm -rf /var/lib/apt/lists/*
#   USER 10001:10001

# Runner configs are read from RUNNERS_DIR; relative paths inside them resolve against BASE_DIR
# (/opt/runwarden), so the examples keep their "examples/..." paths.
COPY examples/runners/ /runwarden/runners/
COPY examples/runners/ /opt/runwarden/examples/runners/
COPY examples/scripts/ /opt/runwarden/examples/scripts/
COPY examples/archives/ /opt/runwarden/examples/archives/

# The base image already runs as this user; repeat it in case a step above switched to root.
USER 10001:10001
