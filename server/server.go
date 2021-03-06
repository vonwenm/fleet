package server

import (
	"encoding/json"
	"errors"

	"github.com/coreos/fleet/third_party/github.com/coreos/go-etcd/etcd"
	log "github.com/coreos/fleet/third_party/github.com/golang/glog"

	"github.com/coreos/fleet/agent"
	"github.com/coreos/fleet/config"
	"github.com/coreos/fleet/engine"
	"github.com/coreos/fleet/event"
	"github.com/coreos/fleet/machine"
	"github.com/coreos/fleet/registry"
	"github.com/coreos/fleet/sign"
	"github.com/coreos/fleet/systemd"
	"github.com/coreos/fleet/version"
)

type Server struct {
	agent   *agent.Agent
	engine  *engine.Engine
	rStream *registry.EventStream
	sStream *systemd.EventStream
	eBus    *event.EventBus

	stop chan bool
}

func New(cfg config.Config) (*Server, error) {
	mach, err := newMachineFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	mgr, err := systemd.NewSystemdManager(systemd.DefaultUnitsDirectory)
	if err != nil {
		return nil, err
	}

	a, err := newAgentFromConfig(mach, cfg, mgr)
	if err != nil {
		return nil, err
	}

	e, err := newEngineFromConfig(mach, cfg)
	if err != nil {
		return nil, err
	}


	sStream := systemd.NewEventStream(mgr)

	rStream, err := newRegistryEventStreamFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	aHandler := agent.NewEventHandler(a)
	eHandler := engine.NewEventHandler(e)

	eBus := event.NewEventBus()
	eBus.AddListener("engine", eHandler)
	eBus.AddListener("agent", aHandler)

	return &Server{a, e, rStream, sStream, eBus, nil}, nil
}

func newEtcdClientFromConfig(cfg config.Config) *etcd.Client {
	c := etcd.NewClient(cfg.EtcdServers)
	c.SetConsistency(etcd.STRONG_CONSISTENCY)
	return c
}

func newRegistryEventStreamFromConfig(cfg config.Config) (*registry.EventStream, error) {
	eClient := newEtcdClientFromConfig(cfg)
	reg := registry.New(eClient, cfg.EtcdKeyPrefix)
	return registry.NewEventStream(eClient, reg)
}

func newMachineFromConfig(cfg config.Config) (*machine.Machine, error) {
	state := machine.MachineState{
		PublicIP: cfg.PublicIP,
		Metadata: cfg.Metadata(),
		Version:  version.Version,
	}

	mach := machine.New(state)
	mach.RefreshState()

	if mach.State().ID == "" {
		return nil, errors.New("unable to determine local machine ID")
	}

	return mach, nil
}

func newAgentFromConfig(mach *machine.Machine, cfg config.Config, mgr *systemd.SystemdManager) (*agent.Agent, error) {
	regClient := newEtcdClientFromConfig(cfg)
	reg := registry.New(regClient, cfg.EtcdKeyPrefix)

	var verifier *sign.SignatureVerifier
	if cfg.VerifyUnits {
		var err error
		verifier, err = sign.NewSignatureVerifierFromAuthorizedKeysFile(cfg.AuthorizedKeysFile)
		if err != nil {
			log.Errorln("Failed to get any key from authorized key file in verify_units mode:", err)
			verifier = sign.NewSignatureVerifier()
		}
	}

	return agent.New(mgr, reg, mach, cfg.AgentTTL, verifier)
}

func newEngineFromConfig(mach *machine.Machine, cfg config.Config) (*engine.Engine, error) {
	regClient := newEtcdClientFromConfig(cfg)
	reg := registry.New(regClient, cfg.EtcdKeyPrefix)
	return engine.New(reg, mach), nil
}

func (s *Server) Run() {
	idx := s.agent.Initialize()

	s.stop = make(chan bool)
	go s.eBus.Listen(s.stop)
	go s.rStream.Stream(idx, s.eBus.Channel, s.stop)
	go s.sStream.Stream(s.eBus.Channel, s.stop)
	go s.agent.Heartbeat(s.stop)

	s.engine.CheckForWork()
}

func (s *Server) Stop() {
	close(s.stop)
}

func (s *Server) Purge() {
	s.agent.Purge()
}

func (s *Server) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct{ Agent *agent.Agent }{Agent: s.agent})
}
