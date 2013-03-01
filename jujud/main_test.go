package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"launchpad.net/gnuflag"
	. "launchpad.net/gocheck"
	"launchpad.net/juju-core/cmd"
	"launchpad.net/juju-core/testing"
	"launchpad.net/juju-core/worker/uniter/jujuc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdtesting "testing"
)

var caCertFile string

func TestPackage(t *stdtesting.T) {
	// Create a CA certificate available for all tests.
	f, err := ioutil.TempFile("", "juju-test-cert")
	if err != nil {
		panic(err)
	}
	_, err = f.WriteString(testing.CACert)
	if err != nil {
		panic(err)
	}
	f.Close()
	caCertFile = f.Name()
	defer os.Remove(caCertFile)

	testing.MgoTestPackage(t)
}

type MainSuite struct{}

var _ = Suite(&MainSuite{})

var flagRunMain = flag.Bool("run-main", false, "Run the application's main function for recursive testing")

// Reentrancy point for testing (something as close as possible to) the jujud
// tool itself.
func TestRunMain(t *stdtesting.T) {
	if *flagRunMain {
		Main(flag.Args())
	}
}

func checkMessage(c *C, msg string, cmd ...string) {
	args := append([]string{"-test.run", "TestRunMain", "-run-main", "--", "jujud"}, cmd...)
	c.Logf("check %#v", args)
	ps := exec.Command(os.Args[0], args...)
	output, err := ps.CombinedOutput()
	c.Logf(string(output))
	c.Assert(err, ErrorMatches, "exit status 2")
	lines := strings.Split(string(output), "\n")
	c.Assert(lines[len(lines)-2], Equals, "error: "+msg)
}

func (s *MainSuite) TestParseErrors(c *C) {
	// Check all the obvious parse errors
	checkMessage(c, "unrecognized command: jujud cavitate", "cavitate")
	msgf := "flag provided but not defined: --cheese"
	checkMessage(c, msgf, "--cheese", "cavitate")

	cmds := []string{"bootstrap-state", "unit", "machine"}
	for _, cmd := range cmds {
		checkMessage(c, msgf, cmd, "--cheese")
	}

	msga := `unrecognized args: ["toastie"]`
	checkMessage(c, msga,
		"bootstrap-state",
		"--instance-id", "ii",
		"--env-config", b64yaml{"blah": "blah"}.encode(),
		"toastie")
	checkMessage(c, msga, "unit",
		"--unit-name", "un/0",
		"toastie")
	checkMessage(c, msga, "machine",
		"--machine-id", "42",
		"toastie")
}

type RemoteCommand struct {
	cmd.CommandBase
	msg string
}

var expectUsage = `usage: remote [options]
purpose: test jujuc

options:
--error (= "")
    if set, fail

here is some documentation
`

func (c *RemoteCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "remote",
		Purpose: "test jujuc",
		Doc:     "here is some documentation",
	}
}

func (c *RemoteCommand) SetFlags(f *gnuflag.FlagSet) {
	f.StringVar(&c.msg, "error", "", "if set, fail")
}

func (c *RemoteCommand) Init(args []string) error {
	return cmd.CheckEmpty(args)
}

func (c *RemoteCommand) Run(ctx *cmd.Context) error {
	if c.msg != "" {
		return errors.New(c.msg)
	}
	fmt.Fprintf(ctx.Stdout, "success!\n")
	return nil
}

func run(c *C, sockPath string, contextId string, exit int, cmd ...string) string {
	args := append([]string{"-test.run", "TestRunMain", "-run-main", "--"}, cmd...)
	c.Logf("check %v %#v", os.Args[0], args)
	ps := exec.Command(os.Args[0], args...)
	ps.Dir = c.MkDir()
	ps.Env = []string{
		fmt.Sprintf("JUJU_AGENT_SOCKET=%s", sockPath),
		fmt.Sprintf("JUJU_CONTEXT_ID=%s", contextId),
		// Code that imports launchpad.net/juju-core/testing needs to
		// be able to find that module at runtime (via build.Import),
		// so we have to preserve that env variable.
		os.ExpandEnv("GOPATH=${GOPATH}"),
	}
	output, err := ps.CombinedOutput()
	if exit == 0 {
		c.Assert(err, IsNil)
	} else {
		c.Assert(err, ErrorMatches, fmt.Sprintf("exit status %d", exit))
	}
	return string(output)
}

type JujuCMainSuite struct {
	sockPath string
	server   *jujuc.Server
}

var _ = Suite(&JujuCMainSuite{})

func (s *JujuCMainSuite) SetUpSuite(c *C) {
	factory := func(contextId, cmdName string) (cmd.Command, error) {
		if contextId != "bill" {
			return nil, fmt.Errorf("bad context: %s", contextId)
		}
		if cmdName != "remote" {
			return nil, fmt.Errorf("bad command: %s", cmdName)
		}
		return &RemoteCommand{}, nil
	}
	s.sockPath = filepath.Join(c.MkDir(), "test.sock")
	srv, err := jujuc.NewServer(factory, s.sockPath)
	c.Assert(err, IsNil)
	s.server = srv
	go func() {
		if err := s.server.Run(); err != nil {
			c.Fatalf("server died: %s", err)
		}
	}()
}

func (s *JujuCMainSuite) TearDownSuite(c *C) {
	s.server.Close()
}

var argsTests = []struct {
	args   []string
	code   int
	output string
}{
	{[]string{"jujuc", "whatever"}, 2, jujudDoc + "error: jujuc should not be called directly\n"},
	{[]string{"remote"}, 0, "success!\n"},
	{[]string{"/path/to/remote"}, 0, "success!\n"},
	{[]string{"remote", "--help"}, 0, expectUsage},
	{[]string{"unknown"}, 1, "error: bad request: bad command: unknown\n"},
	{[]string{"remote", "--error", "borken"}, 1, "error: borken\n"},
	{[]string{"remote", "--unknown"}, 2, "error: flag provided but not defined: --unknown\n"},
	{[]string{"remote", "unwanted"}, 2, `error: unrecognized args: ["unwanted"]` + "\n"},
}

func (s *JujuCMainSuite) TestArgs(c *C) {
	for _, t := range argsTests {
		fmt.Println(t.args)
		output := run(c, s.sockPath, "bill", t.code, t.args...)
		c.Assert(output, Equals, t.output)
	}
}

func (s *JujuCMainSuite) TestNoClientId(c *C) {
	output := run(c, s.sockPath, "", 1, "remote")
	c.Assert(output, Equals, "error: JUJU_CONTEXT_ID not set\n")
}

func (s *JujuCMainSuite) TestBadClientId(c *C) {
	output := run(c, s.sockPath, "ben", 1, "remote")
	c.Assert(output, Equals, "error: bad request: bad context: ben\n")
}

func (s *JujuCMainSuite) TestNoSockPath(c *C) {
	output := run(c, "", "bill", 1, "remote")
	c.Assert(output, Equals, "error: JUJU_AGENT_SOCKET not set\n")
}

func (s *JujuCMainSuite) TestBadSockPath(c *C) {
	badSock := filepath.Join(c.MkDir(), "bad.sock")
	output := run(c, badSock, "bill", 1, "remote")
	err := fmt.Sprintf("error: dial unix %s: .*\n", badSock)
	c.Assert(output, Matches, err)
}
