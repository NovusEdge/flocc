package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

func shipCmd(ctx context.Context, d *deps, name string, args []string) int {
	fs := flag.NewFlagSet("flocc ship", flag.ContinueOnError)
	fs.SetOutput(d.err)
	interactive := fs.Bool("interactive", false, "answer the host's own forms over ssh -t, then copy its answers into the host file")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(d.err, "usage: flocc ship -H <name> [--interactive]")
		return 2
	}
	return ship.ExitCode(shipWith(ctx, d, name, *interactive), "ship")
}

// shipHost ships without a terminal on the host; Part 3's host create calls it. A run whose only
// open item is the Claude login exits 0 on the host, so it returns nil.
func shipHost(ctx context.Context, d *deps, name string) error { return shipWith(ctx, d, name, false) }

func shipWith(ctx context.Context, d *deps, name string, interactive bool) error {
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		return err
	}
	// Ship reads the root itself; asked here first for the friendlier error.
	if _, err := d.repoRoot(); err != nil {
		return fmt.Errorf("flocc ship ships the flocc checkout it runs in; cd into it: %w", err)
	}
	// An io.Reader holding a nil *bytes.Reader is not nil, and Ship refuses answers with --interactive.
	var in io.Reader
	if !interactive {
		if h.TeamKey == "" || h.DigestIssue == "" || len(h.Repos) == 0 {
			return fmt.Errorf("%s has no answers yet; run flocc host add %s", h.Name, h.Name)
		}
		if err := keys.Fill(ctx, keysEnv(d), mapped(listHosts(d)), keys.Slots); err != nil {
			return err
		}
		doc, err := answersDoc(d.store(), h)
		if err != nil {
			return err
		}
		in = bytes.NewReader(doc)
	}
	s, err := d.doShip(ctx, ship.Options{Target: h.Target(), Interactive: interactive, Out: d.out}, in)
	// The host cannot know the name the desktop calls it, which the remedy lines use.
	s.Target = h.Name
	s.Print(d.out)
	if err != nil {
		return err
	}
	if interactive {
		return importHost(ctx, d, h.Name, hosts.Host{})
	}
	return nil
}

// answersDoc is what ship sends on stdin: the host file's answers with the stored keys. Part
// 2a's Ship redacts the document's secrets from its log.
func answersDoc(st keys.Store, h hosts.Host) ([]byte, error) {
	v := map[keys.Slot]string{}
	for _, s := range keys.Slots {
		got, err := st.Get(s)
		if err != nil {
			return nil, err
		}
		if got == "" {
			return nil, fmt.Errorf("the %s is missing", s.Title())
		}
		v[s] = got
	}
	a := h.Answers
	a.NtfyTopic = v[keys.Ntfy]
	a.Secrets = &answers.Secrets{LinearKey: v[keys.Linear], AgentToken: v[keys.Agent], PublishToken: v[keys.Publish]}
	return toml.Marshal(a)
}

func loginCmd(ctx context.Context, d *deps, name string, args []string) int {
	fs := flag.NewFlagSet("flocc login", flag.ContinueOnError)
	fs.SetOutput(d.err)
	email := fs.String("email", "", "the claude.ai account to sign in as")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(d.err, "usage: flocc login -H <name> [--email ADDR]")
		return 2
	}
	// The address goes into the remote command line, which sudo -i hands to a login shell.
	if *email != "" && !ship.ValidEmail(*email) {
		fmt.Fprintf(d.err, "flocc login: %q is not an email address\n", *email)
		return 2
	}
	return ship.ExitCode(loginWith(ctx, d, name, *email), "login")
}

// loginHost is flocc login -H <name> with no --email; Part 3's host create calls it.
func loginHost(ctx context.Context, d *deps, name string) error { return loginWith(ctx, d, name, "") }

// loginWith runs Part 2a's Login, which opens the sign-in URL here and prints the host's summary.
func loginWith(ctx context.Context, d *deps, name, email string) error {
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		return err
	}
	return d.doLogin(ctx, ship.LoginOptions{Target: h.Target(), Name: h.Name, Email: email, Out: d.out})
}
