package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

const sha = "1111111111111111111111111111111111111111"

func repoFake(head string) *host.Fake {
	return host.NewFake().
		On("git bundle list-heads /stage/flocc.bundle", 0, sha+" HEAD\n").
		On("git -C /home/flocc/flocc rev-parse HEAD", 0, head+"\n")
}

func TestRepoCheckComparesBundleHeadAndInstall(t *testing.T) {
	e := testEnv(t, repoFake(sha))
	e.Stage = "/stage"
	if done, detail, _ := (Repo{}).Check(context.Background(), e); done || detail != "flocc not installed" {
		t.Fatalf("detail=%q", detail)
	}
	os.MkdirAll(e.HomePath(".local/bin"), 0o755)
	os.WriteFile(e.HomePath(".local/bin/flocc"), nil, 0o755)
	os.MkdirAll(e.Path(OptDir+"/bin"), 0o755)
	os.WriteFile(e.Path(OptDir+"/bin/flocc-agent"), nil, 0o755)
	if done, detail, _ := (Repo{}).Check(context.Background(), e); done || !strings.HasPrefix(detail, OptDir+" not published at") {
		t.Fatalf("detail=%q", detail)
	}
	os.WriteFile(e.Path(OptDir+"/.flocc-commit"), []byte(sha+"\n"), 0o644)
	if done, _, _ := (Repo{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
	e.Run = repoFake("2222222222222222222222222222222222222222")
	if done, detail, _ := (Repo{}).Check(context.Background(), e); done || !strings.Contains(detail, "bundle at 1111111") {
		t.Fatalf("detail=%q", detail)
	}
}

func TestRepoApplyStopsOnLocalEditsWithGitsMessage(t *testing.T) {
	f := repoFake("").
		On("git -C /home/flocc/flocc fetch -q /stage/flocc.bundle HEAD", 0, "")
	f.Responses["git -C /home/flocc/flocc checkout -q --detach FETCH_HEAD"] = host.Result{
		Stderr: "error: Your local changes to the following files would be overwritten by checkout:\n\tplugin/evals/x.json\n", Code: 1}
	e := testEnv(t, f)
	e.Stage = "/stage"
	os.MkdirAll(e.HomePath(repoRel+"/.git"), 0o755)
	err := (Repo{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "would be overwritten") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("./setup") {
		t.Fatal("setup ran after a failed checkout")
	}
}

func TestCleanSecret(t *testing.T) {
	for in, want := range map[string]string{"lin_api_x\n": "lin_api_x", "  ghp_y\r\n": "ghp_y"} {
		if got, err := CleanSecret(in); err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"LINEAR_KEY=lin_api_x", "a b", "a\nb"} {
		if _, err := CleanSecret(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestSecretsApplyKeepsValidFilesOnEmptyInputAndWritesMode600(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Answers = &answers.Answers{}
	dir := e.HomePath(".config/flocc")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "linear.key"), []byte("lin_old\n"), 0o600)
	scriptedSecrets(e, &answers.Secrets{AgentToken: "ghp_agent\n", PublishToken: "ghp_pub"})
	if err := (Secrets{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "linear.key")); string(b) != "lin_old\n" {
		t.Fatalf("linear.key overwritten: %q", b)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "gh-agent.token"))
	st, _ := os.Stat(filepath.Join(dir, "gh-agent.token"))
	if string(b) != "ghp_agent\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %o", b, st.Mode().Perm())
	}
	if done, _, _ := (Secrets{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}

func TestSecretsApplyReplacesAWorldReadableFileWithMode600(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Answers = &answers.Answers{}
	dir := e.HomePath(".config/flocc")
	os.MkdirAll(dir, 0o700)
	loose := filepath.Join(dir, "linear.key")
	os.WriteFile(loose, []byte("old\n"), 0o644)
	os.Chmod(loose, 0o644)
	scriptedSecrets(e, &answers.Secrets{LinearKey: "lin_new", AgentToken: "a", PublishToken: "p"})
	if err := (Secrets{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(loose)
	if b, _ := os.ReadFile(loose); st.Mode().Perm() != 0o600 || string(b) != "lin_new\n" {
		t.Fatalf("%o %q", st.Mode().Perm(), b)
	}
}

func writeOldSecrets(t *testing.T, e *Env) string {
	t.Helper()
	dir := e.HomePath(".config/flocc")
	os.MkdirAll(dir, 0o700)
	for _, f := range []string{"linear.key", "gh-agent.token", "gh-publish.token"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("old_"+f+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSecretsShipReplacesAFileThatDiffersFromTheAnswers(t *testing.T) {
	e := testEnv(t, host.NewFake())
	dir := writeOldSecrets(t, e)
	scriptedSecrets(e, &answers.Secrets{LinearKey: "old_linear.key", AgentToken: "ghp_new\n", PublishToken: "old_gh-publish.token"})
	done, detail, _ := (Secrets{}).Check(context.Background(), e)
	if done || detail != "gh-agent.token: differs from the answers" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
	if err := RunAll(context.Background(), e, []Step{Secrets{}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "gh-agent.token")
	st, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "ghp_new\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %o", b, st.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "linear.key")); string(b) != "old_linear.key\n" {
		t.Fatalf("linear.key: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestSecretsCheckPassesWhenTheAnswersMatchOrAreEmpty(t *testing.T) {
	e := testEnv(t, host.NewFake())
	dir := writeOldSecrets(t, e)
	scriptedSecrets(e, &answers.Secrets{LinearKey: "old_linear.key\n", AgentToken: "", PublishToken: "  old_gh-publish.token"})
	if done, detail, _ := (Secrets{}).Check(context.Background(), e); !done {
		t.Fatalf("detail=%q", detail)
	}
	if err := RunAll(context.Background(), e, []Step{Secrets{}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "gh-agent.token")); string(b) != "old_gh-agent.token\n" {
		t.Fatalf("gh-agent.token: %q", b)
	}
}

func TestSecretsShipKeepsAFileWhenItsAnswerIsEmpty(t *testing.T) {
	e := testEnv(t, host.NewFake())
	dir := writeOldSecrets(t, e)
	scriptedSecrets(e, &answers.Secrets{LinearKey: "lin_new", AgentToken: ""})
	if err := RunAll(context.Background(), e, []Step{Secrets{}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "linear.key")); string(b) != "lin_new\n" {
		t.Fatalf("linear.key: %q", b)
	}
	for _, f := range []string{"gh-agent.token", "gh-publish.token"} {
		if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "old_"+f+"\n" {
			t.Fatalf("%s: %q", f, b)
		}
	}
}

func TestSecretsApplyRequiresMissingSecrets(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if err := (Secrets{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "Linear API key is required") {
		t.Fatalf("err=%v", err)
	}
}

func TestSecretFileNamesTheHostsFiles(t *testing.T) {
	for id, file := range map[string]string{"linear_key": "linear.key", "agent_token": "gh-agent.token", "publish_token": "gh-publish.token", "x": ""} {
		if got := SecretFile(id); got != file {
			t.Errorf("%s: %q", id, got)
		}
	}
}
