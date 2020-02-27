package test_util

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"code.cloudfoundry.org/tlsconfig"
	"github.com/nats-io/nats.go"
	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

type NATSRunner struct {
	port        int
	natsSession *gexec.Session
	MessageBus  *nats.Conn
	username    string
	password    string
}

type NATSRunnerWithTLS struct {
	NATSRunner
	caFile   string
	certFile string
	keyFile  string
}

func NewNATSRunner(port int) *NATSRunner {
	return &NATSRunner{
		port: port,
	}
}

func NewNATSRunnerWithTLS(port uint16, username, password, caFile, certFile, keyFile string) *NATSRunnerWithTLS {
	return &NATSRunnerWithTLS{
		NATSRunner: NATSRunner{
			port:     int(port),
			username: username,
			password: password,
		},
		caFile:   caFile,
		certFile: certFile,
		keyFile:  keyFile,
	}
}

func (runner *NATSRunner) Start() {
	runner.start("gnatsd", "-p", strconv.Itoa(runner.port))

	var messageBus *nats.Conn
	Eventually(func() error {
		var err error
		messageBus, err = nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", runner.port))
		return err
	}, 5, 0.1).ShouldNot(HaveOccurred())

	runner.MessageBus = messageBus
}

func (runner *NATSRunnerWithTLS) Start() {
	runner.start(
		"gnatsd",
		"-p", strconv.Itoa(runner.port),
		"--tlsverify",
		"--tlscacert", runner.caFile,
		"--tlscert", runner.certFile,
		"--tlskey", runner.keyFile,
	)

	tlsOpts := nats.DefaultOptions
	tlsServers := []string{
		fmt.Sprintf(
			"nats://127.0.0.1:%d",
			runner.port,
		),
	}
	tlsOpts.Servers = tlsServers

	tlsConfig, err := tlsconfig.Build(
		tlsconfig.WithInternalServiceDefaults(),
		tlsconfig.WithIdentityFromFile(runner.certFile, runner.keyFile),
	).Client(
		tlsconfig.WithAuthorityFromFile(runner.caFile),
	)
	Expect(err).NotTo(HaveOccurred())

	tlsOpts.TLSConfig = tlsConfig
	var messageBus *nats.Conn
	Eventually(func() error {
		var err error
		messageBus, err = tlsOpts.Connect()
		return err
	}, 5, 0.1).ShouldNot(HaveOccurred())

	runner.MessageBus = messageBus
}

func (runner *NATSRunner) start(name string, args ...string) {
	if runner.natsSession != nil {
		panic("starting an already started NATS runner!!!")
	}

	_, err := exec.LookPath("gnatsd")
	if err != nil {
		fmt.Println("You need gnatsd installed!")
		os.Exit(1)
	}

	cmd := exec.Command(name, args...)
	sess, err := gexec.Start(
		cmd,
		gexec.NewPrefixedWriter("\x1b[32m[o]\x1b[34m[gnatsd]\x1b[0m ", ginkgo.GinkgoWriter),
		gexec.NewPrefixedWriter("\x1b[91m[e]\x1b[34m[gnatsd]\x1b[0m ", ginkgo.GinkgoWriter),
	)
	Expect(err).NotTo(HaveOccurred(), "Make sure to have gnatsd on your path")

	runner.natsSession = sess
}

func (runner *NATSRunner) Stop() {
	runner.killWithFire()
}

func (runner *NATSRunner) killWithFire() {
	if runner.natsSession != nil {
		runner.natsSession.Kill().Wait(5 * time.Second)
		runner.MessageBus = nil
		runner.natsSession = nil
	}
}
