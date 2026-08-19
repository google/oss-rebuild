// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

const budgetCollection = "scheduler_budget"

// Budgets persists the per-period spend ledgers, one document per scope.
// Spend is recorded via Mutate so that concurrent dispatch runs increment
// rather than overwrite. Mutate edits only existing documents, so callers
// seed a missing ledger with Insert and retry on ErrAlreadyExists.
type Budgets = Resource[scheduler.Budget, string]

func budgetKey(scope string) []string {
	return []string{budgetCollection, scope}
}

func budgetPath(b scheduler.Budget) []string { return budgetKey(b.Scope) }

// NewFirestoreBudgets returns the Firestore-backed spend ledgers.
func NewFirestoreBudgets(c *firestore.Client) Budgets {
	return &firestoreResource[scheduler.Budget, string]{client: c, pathFor: budgetPath, pathForKey: budgetKey}
}

// NewMemoryBudgets returns in-memory spend ledgers for tests.
func NewMemoryBudgets() Budgets {
	return &memoryResource[scheduler.Budget, string]{data: map[string]scheduler.Budget{}, pathFor: budgetPath, pathForKey: budgetKey}
}
