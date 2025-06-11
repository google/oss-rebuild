# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0
FROM ruby:3.1-alpine
ARG REPO_NWO
ENV PAGES_REPO_NWO=${REPO_NWO}
# Install required dependencies
RUN apk add --no-cache build-base git curl
RUN gem install bundler
WORKDIR /srv
COPY Gemfile .
RUN bundle install
# Expose Jekyll site
EXPOSE 4000
CMD bundle exec jekyll serve --host 0.0.0.0 --force_polling --source /site --destination /_site
