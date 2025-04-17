# Rebuild Analyzers

Rebuild Analyzers are subsystems that consume Rebuild attestations to perform additional analysis on artifacts.

## Overview

Rebuild Analyzers extend OSS Rebuild's capabilities beyond simple reproducibility verification while maintaining separation of concerns. Each analyzer consumes attestation data and produces structured findings that can be used for security analysis, quality assessment, or other verification tasks.

## Architecture

### Core Abstraction

An Analyzer comprises:

- _Subscriber_: Takes a rebuild attestation as input and triggers analysis
- _Analyzer_: Operates over the rebuild attestation and produces analysis results or "findings"
- _Storage_: Exposes its analysis using OSS Rebuild's hierarchical identifier scheme

### Analyzer Output

All analyzers produce results that include at minimum:

- Metadata about the analyzer and execution time
- Success/failure status indicating completion
- References to result artifacts (reports, logs, etc.)

## Implementing Custom Analyzers

For more details, see the implementation example at
[`analyzer/example/`](./example).
