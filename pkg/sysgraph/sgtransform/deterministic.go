// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
)

type actionKey struct {
	inputs   []string
	outputs  []string
	execInfo string
	parentID int64
}

func stringsForSorting(allKeys map[int64]actionKey) (map[int64]string, error) {
	res := make(map[int64]string, len(allKeys))
	toCalculate := make(map[int64]struct{})
	for k := range allKeys {
		toCalculate[k] = struct{}{}
	}
	for len(toCalculate) > 0 {
		calculated := false
		for k := range toCalculate {
			parent, ok := res[allKeys[k].parentID]
			if !ok && allKeys[k].parentID != 0 {
				continue
			}
			delete(toCalculate, k)
			calculated = true
			h := sha256.New()
			h.Write([]byte(parent))
			h.Write([]byte(";"))
			h.Write([]byte(allKeys[k].execInfo))
			h.Write([]byte(";"))
			h.Write([]byte(strings.Join(allKeys[k].inputs, ";")))
			h.Write([]byte(";"))
			h.Write([]byte(strings.Join(allKeys[k].outputs, ";")))
			res[k] = hex.EncodeToString(h.Sum(nil))
		}
		if !calculated {
			return nil, fmt.Errorf("failed to calculate strings for sorting due to cyclic graph")
		}
	}
	return res, nil
}

// NewDeterministicSysGraph returns a deterministic sysgraph.
func NewDeterministicSysGraph(ctx context.Context, sg SysGraph) (*DenseSysGraph, error) {
	originalToDense := make(map[int64]int64)
	denseToOriginal := make(map[int64]int64)
	originalActionKeys, err := sgquery.MapAllActions(ctx, sg,
		func(a *sgpb.Action) (int64, actionKey, error) {
			eiDg, err := pbdigest.NewFromMessage(a.GetExecInfo())
			if err != nil {
				return 0, actionKey{}, err
			}
			return a.GetId(), actionKey{
				inputs:   slices.Sorted(maps.Keys(a.GetInputs())),
				outputs:  slices.Sorted(maps.Keys(a.GetOutputs())),
				execInfo: eiDg.String(),
				parentID: a.GetParentActionId(),
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	sortedKeyMap, err := stringsForSorting(originalActionKeys)
	if err != nil {
		return nil, err
	}
	sortedKeyList := slices.SortedFunc(maps.Keys(sortedKeyMap), func(a, b int64) int {
		return strings.Compare(sortedKeyMap[a], sortedKeyMap[b])
	})
	for i, originalKey := range sortedKeyList {
		originalToDense[originalKey] = int64(i + 1)
		denseToOriginal[int64(i+1)] = originalKey
	}

	return &DenseSysGraph{
		original:            sg,
		fromDenseToOriginal: denseToOriginal,
		fromOriginalToDense: originalToDense,
	}, nil
}
