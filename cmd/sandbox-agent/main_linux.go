// Command sandbox-agent is PID 1 inside every sandbox microVM. It serves the
// guest.v1.Guest gRPC API on vsock port 52.
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"time"

	"github.com/mdlayher/vsock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/guest/initd"
	"github.com/karthikponna/tryboxi/guest/proc"
	"github.com/karthikponna/tryboxi/guest/server"
)

const vsockPort = 52

func main() {
	if initd.InInitramfs() {
		initd.Console()
		log.SetFlags(log.Lmicroseconds)
		if err := initd.SwitchRoot(); err != nil {
			log.Printf("sandbox-agent: switch root failed: %v", err)
			time.Sleep(2 * time.Second)
			initd.Poweroff(false)
		}
		return
	}

	listen := flag.String("listen", "", "listen on a TCP/unix address instead of vsock (testing only)")
	flag.Parse()

	pid1 := os.Getpid() == 1
	if pid1 {
		initd.Setup()
		initd.Console()
	}
	log.SetFlags(log.Lmicroseconds)
	log.SetPrefix("sandbox-agent: ")
	proc.StartReaper()

	var lis net.Listener
	var err error
	switch {
	case *listen != "":
		lis, err = net.Listen("tcp", *listen)
	default:
		for i := 0; i < 50; i++ {
			lis, err = vsock.Listen(vsockPort, nil)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err != nil {
		log.Printf("listen: %v", err)
		if pid1 {
			initd.Poweroff(false)
		}
		os.Exit(1)
	}

	srv := server.New()
	g := grpc.NewServer(
		grpc.UnaryInterceptor(srv.Unary()),
		grpc.StreamInterceptor(srv.Stream()),
		grpc.MaxRecvMsgSize(64<<20),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
	)
	guestv1.RegisterGuestServer(g, srv)
	go srv.RunDeadman()
	log.Printf("ready version=%s port=%d", server.Version, vsockPort)
	if err := g.Serve(lis); err != nil {
		log.Printf("serve: %v", err)
	}
	if pid1 {
		initd.Poweroff(false)
	}
}
