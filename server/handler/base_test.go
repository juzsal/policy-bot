// Copyright 2026 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"context"
	"testing"

	"github.com/google/go-github/v89/github"
	"github.com/palantir/go-githubapp/appconfig"
	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/pull"
	"github.com/palantir/policy-bot/pull/pulltest"
	"github.com/stretchr/testify/assert"
)

func TestStackDestinationTarget(t *testing.T) {
	on := &PullEvaluationOptions{PostStackDestinationStatus: true}
	off := &PullEvaluationOptions{PostStackDestinationStatus: false}

	cases := []struct {
		name    string
		opts    *PullEvaluationOptions
		stack   *pull.StackInfo
		base    string
		wantDst string
		wantOk  bool
	}{
		{"flag off", off, &pull.StackInfo{Destination: "main", Position: 3}, "feat-b", "", false},
		{"no stack", on, nil, "feat-b", "", false},
		{"bottom PR", on, &pull.StackInfo{Destination: "main", Position: 1}, "main", "", false},
		{"upper PR", on, &pull.StackInfo{Destination: "main", Position: 3}, "feat-b", "main", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst, ok := stackDestinationTarget(tc.opts, tc.stack, tc.base)
			assert.Equal(t, tc.wantOk, ok)
			assert.Equal(t, tc.wantDst, dst)
		})
	}
}

func TestRebasedContextBranches(t *testing.T) {
	inner := &pulltest.Context{
		OwnerValue:     "testorg",
		BranchBaseName: "feat-b",
		BranchHeadName: "feat-c",
	}
	rc := &rebasedContext{Context: inner, base: "main"}

	base, head := rc.Branches()
	assert.Equal(t, "main", base, "base is overridden with the destination")
	assert.Equal(t, "feat-c", head, "head passes through unchanged")
	assert.Equal(t, "testorg", rc.RepositoryOwner(), "other methods pass through")
}

func TestEvaluateStackDestination(t *testing.T) {
	cases := []struct {
		name    string
		flag    bool
		base    string
		stack   *pull.StackInfo
		wantRef string
	}{
		{"flag off", false, "feat-b", &pull.StackInfo{Destination: "main", Position: 3, Size: 4}, ""},
		{"not a stack", true, "feat-b", nil, ""},
		{"bottom PR", true, "main", &pull.StackInfo{Destination: "main", Position: 1, Size: 4}, ""},
		{"upper PR", true, "feat-b", &pull.StackInfo{Destination: "main", Position: 3, Size: 4}, "main"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loader := &recordingLoader{}
			b := &Base{
				PullOpts: &PullEvaluationOptions{
					StatusCheckContext:         "policy-bot",
					PostStackDestinationStatus: tc.flag,
				},
				ConfigFetcher: &ConfigFetcher{Loader: loader, SeenPolicyCache: NewSeenPolicyCache()},
			}
			primary := &EvalContext{
				Options:   b.PullOpts,
				PublicURL: "https://policy-bot.example.com",
				PullContext: &pulltest.Context{
					OwnerValue:     "testorg",
					RepoValue:      "testrepo",
					NumberValue:    7,
					StateValue:     "open",
					HeadSHAValue:   "abc123",
					BranchBaseName: tc.base,
					StackValue:     tc.stack,
				},
			}

			b.evaluateStackDestination(context.Background(), primary, common.TriggerAll)

			assert.Equal(t, tc.wantRef, loader.ref, "loads the destination policy only for an upper stack member")
		})
	}
}

type recordingLoader struct{ ref string }

func (l *recordingLoader) LoadConfig(ctx context.Context, client *github.Client, owner, repo, ref string) (appconfig.Config, error) {
	l.ref = ref
	return appconfig.Config{}, nil
}
