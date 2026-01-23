#!/usr/bin/env bash

# Script to run checks before committing.
# To install run ./scripts/install_precommit.sh

# Initialize PASS variable
PASS=true

echo "Running go mod tidy..."
if ! go mod tidy; then
  printf "\033[0;30m\033[41mgo mod tidy FAILED\033[0m\n"
  PASS=false
fi

echo "Running addlicense..."
if [ -f "./.hooks/addlicense" ]; then
  if ! ./.hooks/addlicense; then
    printf "\033[0;30m\033[41maddlicense FAILED\033[0m\n"
    PASS=false
  fi
else
  echo "addlicense script not found at ./.hooks/addlicense, skipping..."
fi

echo "Running go build ./..."
if ! go build ./...; then
  printf "\033[0;30m\033[41mgo build FAILED\033[0m\n"
  PASS=false
fi

echo "Running go test ./..."
if ! go test ./...; then
  printf "\033[0;30m\033[41mgo test FAILED\033[0m\n"
  PASS=false
fi

echo "Running go vet ./..."
if ! go vet ./...; then
  printf "\033[0;30m\033[41mgo vet FAILED\033[0m\n"
  PASS=false
fi

echo "Running gofmt..."
if ! gofmt -s -w .; then
  printf "\033[0;30m\033[41mgofmt FAILED\033[0m\n"
  PASS=false
fi

echo "Running goimports..."
if ! go run golang.org/x/tools/cmd/goimports -w .; then
  printf "\033[0;30m\033[41mgoimports FAILED\033[0m\n"
  PASS=false
fi

# Check if any changes were made by formatters/tidy
if ! git diff --exit-code &>/dev/null; then
  printf "\033[0;30m\033[41mPrecommit made changes to source. Please check the changes and re-stage files.\033[0m\n"
  PASS=false
fi

if ! $PASS; then
  printf "\033[0;30m\033[41mCOMMIT FAILED\033[0m\n"
  exit 1
else
  printf "\033[0;30m\033[42mCOMMIT SUCCEEDED\033[0m\n"
fi