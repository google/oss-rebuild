// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import "time"

// GCSObjectEvent represents a subset of the GCS object metadata structure
// sent in the JSON_API_V1 payload format.
// Fields are based on the GCS Object resource: https://cloud.google.com/storage/docs/json_api/v1/objects#resource
type GCSObjectEvent struct {
	Name       string     `json:"name"`        // The name of the object
	Bucket     string     `json:"bucket"`      // The name of the bucket containing this object
	Generation string     `json:"generation"`  // Use string as generation can be very large
	Created    *time.Time `json:"timeCreated"` // Object creation time
	Updated    *time.Time `json:"updated"`     // Last metadata update time
	Size       string     `json:"size"`        // Object size in bytes (as a string)
}
