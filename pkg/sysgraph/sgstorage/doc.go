// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgstorage provides functions to load and store sysgraphs.
//
// The sysgraph is stored on disk in the following structure:
//
//	<graph_path>/
//	  rdb.pb
//	  a/0.pb
//	  a/1.pb
//	  ...
//	  b/0.pb
//	  b/1.pb
//	  ...
//	  ...
//
// The rdb.pb file contains a ResourceDB proto. It is a map of Resource message digests to Resource messages.
// Optionally the rdb.textproto may be present with the textproto representation of the ResourceDB proto.
// Optionally the rdb.json may be present with the json representation of the ResourceDB proto.
//
// The a/i.pb contains the Action proto with id = i.
// Optionally a/1.textproto may be present with the textproto representation of the Action proto.
// Optionally a/1.json may be present with the json representation of the Action proto.
//
// To build a sysgraph at runtime use the SysGraphBuilder.
package sgstorage
