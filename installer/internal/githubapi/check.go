package githubapi

import (
	"context"
	"errors"
	"time"
)

type Target struct{ Slug, Base string }

type TokenCheck struct {
	Expires  time.Time // zero: never expires
	Problems []string
}

// CheckAgent passes a token that reads every repo and can push to none: agents run gh with it.
func CheckAgent(ctx context.Context, c *Client, repos []Target) (TokenCheck, error) {
	return check(ctx, c, repos, func(t Target) (string, error) {
		writes, err := c.CanWriteContents(ctx, t.Slug)
		if err != nil || !writes {
			return "", err
		}
		return t.Slug + ": the token can push; set Contents to Read-only", nil
	})
}

// CheckPublish passes a token that can push to and open pull requests in every repo.
func CheckPublish(ctx context.Context, c *Client, repos []Target) (TokenCheck, error) {
	return check(ctx, c, repos, func(t Target) (string, error) {
		writes, err := c.CanWriteContents(ctx, t.Slug)
		if err != nil {
			return "", err
		}
		if !writes {
			return t.Slug + ": the token cannot push; set Contents to Read and write", nil
		}
		pulls, err := c.CanOpenPulls(ctx, t.Slug, t.Base)
		if err != nil || pulls {
			return "", err
		}
		return t.Slug + ": the token cannot open pull requests; set Pull requests to Read and write", nil
	})
}

func check(ctx context.Context, c *Client, repos []Target, probe func(Target) (string, error)) (TokenCheck, error) {
	var out TokenCheck
	exp, err := c.Expiry(ctx)
	if err != nil {
		return out, err
	}
	out.Expires = exp
	for _, t := range repos {
		if _, err := c.Repo(ctx, t.Slug); errors.Is(err, ErrNotVisible) {
			out.Problems = append(out.Problems, t.Slug+": the token cannot read it; add it under Repository access")
			continue
		} else if err != nil {
			return out, err
		}
		problem, err := probe(t)
		if err != nil {
			return out, err
		}
		if problem != "" {
			out.Problems = append(out.Problems, problem)
		}
	}
	return out, nil
}
