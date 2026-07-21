// Copyright 2018 Palantir Technologies, Inc.
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

	"github.com/google/go-github/v89/github"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/pull"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

const (
	LogKeyGitHubSHA = "github_sha"
)

type Base struct {
	githubapp.ClientCreator

	Installations githubapp.InstallationsService
	GlobalCache   pull.GlobalCache
	ConfigFetcher *ConfigFetcher
	BaseConfig    *baseapp.HTTPConfig
	PullOpts      *PullEvaluationOptions

	AppName string
}

// PostStatus posts a GitHub commit status with consistent logging.
func PostStatus(ctx context.Context, client *github.Client, owner, repo, ref string, status github.RepoStatus) error {
	zerolog.Ctx(ctx).Info().Msgf("Setting %q status on %s to %s: %s", status.GetContext(), ref, status.GetState(), status.GetDescription())
	_, _, err := client.Repositories.CreateStatus(ctx, owner, repo, ref, status)
	return errors.WithStack(err)
}

func (b *Base) PreparePRContext(ctx context.Context, installationID int64, pr *github.PullRequest) (context.Context, zerolog.Logger) {
	ctx, logger := githubapp.PreparePRContext(ctx, installationID, pr.GetBase().GetRepo(), pr.GetNumber())

	logger = logger.With().Str(LogKeyGitHubSHA, pr.GetHead().GetSHA()).Logger()
	ctx = logger.WithContext(ctx)

	return ctx, logger
}

func (b *Base) NewEvalContext(ctx context.Context, installationID int64, loc pull.Locator) (*EvalContext, error) {
	client, err := b.NewInstallationClient(installationID)
	if err != nil {
		return nil, err
	}

	v4client, err := b.NewInstallationV4Client(installationID)
	if err != nil {
		return nil, err
	}

	mbrCtx := NewCrossOrgMembershipContext(ctx, client, loc.Owner, b.Installations, b.ClientCreator)
	prctx, err := pull.NewGitHubContext(ctx, mbrCtx, b.GlobalCache, client, v4client, loc)
	if err != nil {
		return nil, err
	}

	baseBranch, _ := prctx.Branches()
	owner := prctx.RepositoryOwner()
	repository := prctx.RepositoryName()

	fetchedConfig := b.ConfigFetcher.ConfigForRepositoryBranch(ctx, client, owner, repository, baseBranch)

	return &EvalContext{
		Client:   client,
		V4Client: v4client,

		Options:   b.PullOpts,
		PublicURL: b.BaseConfig.PublicURL,

		PullContext: prctx,
		Config:      fetchedConfig,
	}, nil
}

func (b *Base) Evaluate(ctx context.Context, installationID int64, trigger common.Trigger, loc pull.Locator) error {
	evalCtx, err := b.NewEvalContext(ctx, installationID, loc)
	if err != nil {
		return errors.Wrap(err, "failed to create evaluation context")
	}
	if err := evalCtx.Evaluate(ctx, trigger); err != nil {
		return err
	}
	b.evaluateStackDestination(ctx, evalCtx, trigger)
	return nil
}

// stackDestinationTarget returns the branch to post a destination status for,
// and false when the pull request is not an upper member of a stack.
func stackDestinationTarget(opts *PullEvaluationOptions, stack *pull.StackInfo, base string) (string, bool) {
	if !opts.PostStackDestinationStatus || stack == nil {
		return "", false
	}
	if stack.Destination == "" || stack.Destination == base {
		return "", false
	}
	return stack.Destination, true
}

// rebasedContext presents a pull request as though it targets base. It overrides
// the base branch that Branches reports and passes everything else through.
type rebasedContext struct {
	pull.Context
	base string
}

func (c *rebasedContext) Branches() (string, string) {
	_, head := c.Context.Branches()
	return c.base, head
}

// evaluateStackDestination posts the stack destination status for an upper
// member of a native stack. It evaluates the pull request as though it targets
// the destination: the destination branch's policy, with the base seen as the
// destination. It is a no-op when the option is off or the pull request is not
// an upper stack member. Failures are logged, not returned.
func (b *Base) evaluateStackDestination(ctx context.Context, primary *EvalContext, trigger common.Trigger) {
	if !b.PullOpts.PostStackDestinationStatus {
		return
	}

	logger := zerolog.Ctx(ctx)

	sc, ok := primary.PullContext.(pull.StackContext)
	if !ok {
		return
	}
	stack, err := sc.Stack()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to load stack info for destination status")
		return
	}

	base, _ := primary.PullContext.Branches()
	destination, ok := stackDestinationTarget(b.PullOpts, stack, base)
	if !ok {
		return
	}

	owner := primary.PullContext.RepositoryOwner()
	repository := primary.PullContext.RepositoryName()

	destCtx := &EvalContext{
		Client:      primary.Client,
		V4Client:    primary.V4Client,
		Options:     primary.Options,
		PublicURL:   primary.PublicURL,
		PullContext: &rebasedContext{Context: primary.PullContext, base: destination},
		Config:      b.ConfigFetcher.ConfigForRepositoryBranch(ctx, primary.Client, owner, repository, destination),
		Secondary:   true,
	}
	if err := destCtx.EvaluateForStatus(ctx, trigger); err != nil {
		logger.Error().Err(err).Msg("Failed to evaluate stack destination policy")
	}
}
