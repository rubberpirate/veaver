package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Command struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Input   string   `json:"input,omitempty"`
}
type Runner interface {
	Run(context.Context, Command) (string, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, c Command) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Program, c.Args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if c.Input != "" {
		cmd.Stdin = strings.NewReader(c.Input)
	}
	b, e := cmd.CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s: %w: %s", c.Program, e, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
