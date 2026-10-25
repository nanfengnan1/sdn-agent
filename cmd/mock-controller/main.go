package main

import (
	"flag"
	"log"
	"net"
	"os"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/controller"
	"sdn-agent/internal/store"
	"sdn-agent/internal/transport"
)

func main() {
	addr := flag.String("listen", ":9010", "listen")
	path := flag.String("state", "/var/lib/sdn-controller/nodes.json", "persistent identity registry")
	insecure := flag.Bool("insecure", false, "allow lab plaintext")
	cert := flag.String("cert", "", "TLS cert")
	key := flag.String("key", "", "TLS key")
	flag.Parse()
	db, e := store.NewFile(*path)
	if e != nil {
		log.Fatal(e)
	}
	s, e := transport.Server(os.Getenv("SDN_TOKEN"), *cert, *key, *insecure)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterControllerServer(s, &controller.Controller{Store: db})
	l, e := net.Listen("tcp", *addr)
	if e != nil {
		log.Fatal(e)
	}
	log.Fatal(s.Serve(l))
}
