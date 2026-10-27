package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/adapters/vpp"
	"sdn-agent/internal/agent"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/config"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
	"sdn-agent/internal/transport"
	"syscall"
	"time"
)

type identity struct {
	NodeID, RegistrationID string
	Assigned               bool
}

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	opts, err := config.Parse(os.Args[1:], os.LookupEnv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if opts.CheckOnly {
		return json.NewEncoder(os.Stdout).Encode(opts.Config.Redacted())
	}
	listen, endpoint, controller := &opts.Agent.Listen, &opts.Agent.Endpoint, &opts.Agent.Controller
	dir, redisAddr, useFRR := &opts.Agent.StateDir, &opts.Agent.Redis, &opts.Agent.FRR
	cert, key, ca, insecure := &opts.Agent.Cert, &opts.Agent.Key, &opts.Agent.CA, &opts.Agent.Insecure
	token := opts.Agent.Token
	if e := os.MkdirAll(*dir, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(*dir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("another agent owns state-dir: %w", e)
	}
	idfile := filepath.Join(*dir, "identity.json")
	var id identity
	b, e := os.ReadFile(idfile)
	if os.IsNotExist(e) {
		id = identity{NodeID: model.ID(), RegistrationID: model.ID()}
	} else if e != nil {
		return e
	} else if e = json.Unmarshal(b, &id); e != nil {
		return e
	}
	saveID := func() error {
		b, _ := json.Marshal(id)
		tmp := idfile + ".tmp"
		if e := os.WriteFile(tmp, b, 0600); e != nil {
			return e
		}
		f, e := os.OpenFile(tmp, os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return e
		}
		return os.Rename(tmp, idfile)
	}
	if e = saveID(); e != nil {
		return e
	}
	drivers := []backend.Driver{&linux.Driver{Runner: backend.Exec{}}}
	if opts.Agent.Dataplane == "vpp" {
		client := vpp.NewClient(opts.VPP.AdapterSocket)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.Call(ctx, "health", nil, nil)
		cancel()
		if err != nil {
			return err
		}
		drivers = []backend.Driver{&vpp.Driver{Linux: &linux.Driver{Runner: backend.Exec{}}, RPC: client}, &vpp.RoutingDriver{Driver: &frr.Driver{Runner: backend.Exec{}}}}
	} else if *useFRR {
		drivers = append(drivers, &frr.Driver{Runner: backend.Exec{}})
	}
	registry, e := backend.New(drivers...)
	if e != nil {
		return e
	}
	instance := model.ID()
	var client pb.ControllerClient
	if *controller != "" {
		cc, e := transport.Dial(*controller, token, *ca, *insecure)
		if e != nil {
			return e
		}
		defer cc.Close()
		client = pb.NewControllerClient(cc)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r, err := client.Register(ctx, &pb.Registration{ProposedNodeId: id.NodeID, RegistrationId: id.RegistrationID, InstanceId: instance, Endpoint: *endpoint, Capabilities: registry.Capabilities(id.NodeID)})
		cancel()
		if err != nil {
			if !id.Assigned {
				return fmt.Errorf("initial registration: %w", err)
			}
			log.Printf("controller offline, using persisted identity: %v", err)
		} else {
			if id.Assigned && id.NodeID != r.NodeId {
				return fmt.Errorf("controller reassigned established identity; refusing to attach old journal")
			}
			id.NodeID = r.NodeId
			id.Assigned = true
			if e = saveID(); e != nil {
				return e
			}
		}
	}
	var db store.Store
	if *redisAddr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, e = store.NewRedis(ctx, *redisAddr, "sdn/"+id.NodeID+"/")
	} else {
		db, e = store.NewFile(filepath.Join(*dir, "journal.json"))
	}
	if e != nil {
		return e
	}
	if e = agent.CheckDataplaneBinding(context.Background(), db, opts.Agent.Dataplane, registry); e != nil {
		return e
	}
	a, e := agent.New(id.NodeID, db, registry)
	if e != nil {
		return e
	}
	a.InstanceID = instance
	a.Start()
	defer a.Close()
	server, e := transport.Server(token, *cert, *key, *insecure)
	if e != nil {
		return e
	}
	pb.RegisterAgentServer(server, a)
	ln, e := net.Listen("tcp", *listen)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); server.Stop() }()
	if client != nil {
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, e := client.Heartbeat(hctx, &pb.Heartbeat{NodeId: id.NodeID, InstanceId: instance, Endpoint: *endpoint, Revision: a.Revision(hctx)})
				cancel()
				if e == nil {
					a.SetNodeState("RUNNING")
				} else {
					a.SetNodeState("DOWN")
					rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
					r, err := client.Register(rctx, &pb.Registration{ProposedNodeId: id.NodeID, RegistrationId: id.RegistrationID, InstanceId: instance, Endpoint: *endpoint, Capabilities: registry.Capabilities(id.NodeID)})
					rcancel()
					if err == nil && r.NodeId != id.NodeID {
						log.Print("controller identity mismatch; stopping")
						stop()
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	} else {
		a.SetNodeState("RUNNING")
	}
	log.Printf("node_id=%s instance_id=%s listen=%s", id.NodeID, instance, *listen)
	return server.Serve(ln)
}
