// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/pkg/errors"
)

type Migration struct {
	CollectionGroup string
	Transform       func(*firestore.DocumentSnapshot) ([]firestore.Update, error)
}

var ErrSkip = errors.New("skip")
var All = map[string]Migration{
	"01-attempts-native-creation-time": {
		CollectionGroup: "attempts",
		Transform: func(doc *firestore.DocumentSnapshot) ([]firestore.Update, error) {
			const field = "created"
			val, err := doc.DataAt(field)
			if err != nil && strings.Contains(err.Error(), "no field") {
				return nil, ErrSkip
			} else if err != nil {
				return nil, errors.Wrapf(err, "%+v getting field", doc.Ref.Path)
			}
			switch val := val.(type) {
			case int64:
				new := time.Unix(val, 0)
				if new.After(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)) {
					new = time.UnixMilli(val)
				}
				new = new.UTC()
				if new.Before(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
					return nil, errors.Errorf("unexpected date: %v", new)
				}
				return []firestore.Update{
					{Path: field, Value: new},
				}, nil
			case time.Time:
				return nil, ErrSkip
			default:
				return nil, errors.New("unexpected type")
			}
		},
	},
	"02-attempts-native-started-time": {
		CollectionGroup: "attempts",
		Transform: func(doc *firestore.DocumentSnapshot) ([]firestore.Update, error) {
			const field = "started"
			val, err := doc.DataAt(field)
			if err != nil && strings.Contains(err.Error(), "no field") {
				return nil, ErrSkip
			} else if err != nil {
				return nil, errors.Wrapf(err, "%+v getting field", doc.Ref.Path)
			}
			switch val := val.(type) {
			case int64:
				new := time.Unix(val, 0)
				if new.After(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)) {
					new = time.UnixMilli(val)
				}
				new = new.UTC()
				if new.Before(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
					return nil, errors.Errorf("unexpected date: %v", new)
				}
				return []firestore.Update{
					{Path: field, Value: new},
				}, nil
			case time.Time:
				return nil, ErrSkip
			default:
				return nil, errors.New("unexpected type")
			}
		},
	},
	"03-runs-native-created-time": {
		CollectionGroup: "runs",
		Transform: func(doc *firestore.DocumentSnapshot) ([]firestore.Update, error) {
			const field = "created"
			val, err := doc.DataAt(field)
			if err != nil && strings.Contains(err.Error(), "no field") {
				return nil, ErrSkip
			} else if err != nil {
				return nil, errors.Wrapf(err, "%+v getting field", doc.Ref.Path)
			}
			switch val := val.(type) {
			case int64:
				new := time.Unix(val, 0)
				if new.After(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)) {
					new = time.UnixMilli(val)
				}
				new = new.UTC()
				if new.Before(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
					return nil, errors.Errorf("unexpected date: %v", new)
				}
				return []firestore.Update{
					{Path: field, Value: new},
				}, nil
			case time.Time:
				return nil, ErrSkip
			default:
				return nil, errors.New("unexpected type")
			}
		},
	},
}
