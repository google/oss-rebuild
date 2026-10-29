// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

// Purpose is what one shell call the model made was for. A compound command
// takes the purpose of its most consequential part, in the order listed.
type Purpose string

const (
	PurposePackage Purpose = "package" // produce the artifact
	PurposeBuild   Purpose = "build"   // compile, transpile, or generate toward the artifact
	PurposeDeps    Purpose = "deps"    // install or fetch dependencies or toolchains
	PurposeSource  Purpose = "source"  // move or clean the checkout
	PurposeEdit    Purpose = "edit"    // change build inputs or files in the tree
	PurposeSetup   Purpose = "setup"   // configure the environment outside the tree
	PurposeCompare Purpose = "compare" // put the candidate and upstream side by side
	PurposeInspect Purpose = "inspect" // observe only
)

// Purposes lists every Purpose, most consequential first.
var Purposes = []Purpose{PurposePackage, PurposeBuild, PurposeDeps, PurposeSource, PurposeEdit, PurposeSetup, PurposeCompare, PurposeInspect}

// SessionPurposes is a session's purposes.json object in the sessions
// bucket, beside its messages/: the purpose of each shell call in the
// transcript, classified by a model once the session has ended.
type SessionPurposes struct {
	Model string        `json:"model"`
	Usage *TokenUsage   `json:"usage,omitempty"` // the classifying call's tokens
	Calls []CallPurpose `json:"calls"`
}

// CallPurpose is the purpose of one function call of the transcript.
type CallPurpose struct {
	Object  string    `json:"object"` // the transcript object, relative to messages/
	Call    int       `json:"call"`   // index among the object's function calls
	Purpose Purpose   `json:"purpose"`
	All     []Purpose `json:"all"` // every purpose a part of the call serves, Purpose included, in the order of Purposes
}

// SessionPurposesObject names a session's SessionPurposes object in the
// sessions bucket.
func SessionPurposesObject(sessionID string) string { return sessionID + "/purposes.json" }
