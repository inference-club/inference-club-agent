FROM python:3.13.3-slim AS base
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    POETRY_VERSION='2.1.2'

# ARG SOURCE_TAG
# ENV SOURCE_TAG=$SOURCE_TAG

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      build-essential \
      curl \
      ffmpeg && \
    rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --home-dir /code --shell /bin/bash app

WORKDIR /code
RUN pip install "poetry==$POETRY_VERSION"
COPY poetry.lock pyproject.toml /code/
# RUN poetry lock

FROM base AS prod
RUN POETRY_VIRTUALENVS_CREATE=false poetry install --only main
COPY . /code
RUN chown -R app:app /code
USER app

FROM base AS local
RUN apt-get update && apt-get install -y procps ffmpeg # for pkill and ffmpeg
RUN POETRY_VIRTUALENVS_CREATE=false poetry install --with test,dev
USER app
