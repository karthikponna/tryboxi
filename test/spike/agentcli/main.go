// Command agentcli talks to a sandbox-agent started with -listen (TCP) for
// debugging the guest agent outside a VM.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/guestclient"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5252", "agent address")
	cmd := flag.String("c", "echo hello", "command")
	user := flag.String("user", "", "user")
	flag.Parse()
	c, err := guestclient.Dial(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", *addr)
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.WaitReady(ctx); err != nil {
		log.Fatal(err)
	}
	id := fmt.Sprintf("exe_%d", time.Now().UnixNano())
	st, err := c.Exec(ctx, &guestv1.ExecRequest{ExecId: id, Command: *cmd, User: *user, TimeoutS: 20})
	if err != nil {
		log.Fatal(err)
	}
	for {
		f, err := st.Recv()
		if err == io.EOF {
			log.Printf("stream EOF")
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		switch k := f.Kind.(type) {
		case *guestv1.ExecFrame_Started:
			log.Printf("seq=%d started pid=%d", f.Seq, k.Started.Pid)
		case *guestv1.ExecFrame_Output:
			log.Printf("seq=%d %s %q", f.Seq, k.Output.Stream, k.Output.Data)
		case *guestv1.ExecFrame_Exit:
			log.Printf("seq=%d exit=%+v", f.Seq, k.Exit)
			os.Exit(0)
		}
	}
}
