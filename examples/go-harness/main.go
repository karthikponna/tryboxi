// Command go-harness is a tiny custom agent harness: it gives a repo to a
// sandbox, runs its tests, tries a fix in two forks in parallel, and keeps
// the fork whose tests pass. Run with the daemon up:
//
//	go run ./examples/go-harness -repo /path/to/repo
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/karthikponna/tryboxi/pkg/client"
)

func main() {
	repo := flag.String("repo", "test/fixtures/node-app", "repo to test")
	flag.Parse()
	abs, _ := filepath.Abs(*repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, err := client.New(client.Options{Name: "go-harness"})
	if err != nil {
		log.Fatal(err)
	}
	t0 := time.Now()
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{
		Template:  "auto",
		Workspace: &client.WorkspaceSpec{HostPath: abs, Mode: "sandbox"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), sb.ID)
	fmt.Printf("sandbox %s ready in %s (from %s)\n", sb.ID, time.Since(t0).Round(time.Millisecond), sb.BootedFrom)

	run := func(id, cmd string) client.Execution {
		e, err := c.Exec(ctx, id, client.ExecRequest{Cmd: cmd, TimeoutS: 300})
		if err != nil {
			log.Fatal(err)
		}
		return e
	}
	e := run(sb.ID, "npm test 2>&1 | tail -5")
	fmt.Printf("baseline tests: exit=%d\n%s\n", *e.ExitCode, e.Stdout)

	// Break the code, then try two candidate fixes in parallel forks.
	run(sb.ID, "sed -i 's/a + b/a - b/' sum.js")
	fork, err := c.Fork(ctx, sb.ID, 2)
	if err != nil {
		log.Fatal(err)
	}
	fixes := []string{"sed -i 's/a - b/a * b/' sum.js", "sed -i 's/a - b/Number(a) + Number(b)/' sum.js"}
	var mu sync.Mutex
	winner := ""
	var wg sync.WaitGroup
	for i, f := range fork.Sandboxes {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			defer c.DestroySandbox(context.Background(), id)
			run(id, fixes[i])
			e := run(id, "npm test >/dev/null 2>&1; echo $?")
			fmt.Printf("fork %s with fix %q -> tests exit %s", id, fixes[i], e.Stdout)
			if e.Stdout == "0\n" {
				mu.Lock()
				winner = id
				mu.Unlock()
				d, _ := c.Diff(ctx, id)
				fmt.Printf("winning diff (%s):\n%s\n", d.ArtifactID, d.Patch)
			}
		}(i, f.ID)
	}
	wg.Wait()
	fmt.Println("winner:", winner, "(apply it to the host with: sandboxctl apply", winner+")")
}
