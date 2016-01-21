package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/cloudfoundry-incubator/cc-uploader/ccclient"
	"github.com/cloudfoundry-incubator/cc-uploader/handlers"
	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/cf_http"
	"github.com/cloudfoundry-incubator/consuladapter"
	"github.com/cloudfoundry-incubator/locket"
	"github.com/cloudfoundry/dropsonde"
	"github.com/hashicorp/consul/api"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
)

var serverAddress = flag.String(
	"address",
	"0.0.0.0:9090",
	"Specifies the address to bind to",
)

var skipCertVerify = flag.Bool(
	"skipCertVerify",
	false,
	"Skip SSL certificate verification",
)

var ccJobPollingInterval = flag.Duration(
	"ccJobPollingInterval",
	1*time.Second,
	"the interval between job polling requests",
)

var communicationTimeout = flag.Duration(
	"communicationTimeout",
	30*time.Second,
	"Timeout applied to all HTTP requests.",
)

var dropsondePort = flag.Int(
	"dropsondePort",
	3457,
	"port the local metron agent is listening on",
)

var consulCluster = flag.String(
	"consulCluster",
	"",
	"Consul Agent URL",
)

const (
	ccUploadDialTimeout         = 10 * time.Second
	ccUploadKeepAlive           = 30 * time.Second
	ccUploadTLSHandshakeTimeout = 10 * time.Second
	dropsondeOrigin             = "cc_uploader"
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	cf_http.Initialize(*communicationTimeout)

	logger, reconfigurableSink := cf_lager.New("cc-uploader")

	initializeDropsonde(logger)
	consulClient, err := consuladapter.NewClient(*consulCluster)
	if err != nil {
		logger.Fatal("new-client-failed", err)
	}

	registrationRunner := initializeRegistrationRunner(logger, consulClient, *serverAddress, clock.NewClock())

	members := grouper.Members{
		{"cc-uploader", initializeServer(logger)},
		{"registration-runner", registrationRunner},
	}

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	monitor := ifrit.Invoke(sigmon.New(group))
	logger.Info("ready")

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
}

func initializeDropsonde(logger lager.Logger) {
	dropsondeDestination := fmt.Sprint("localhost:", *dropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeServer(logger lager.Logger) ifrit.Runner {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   ccUploadDialTimeout,
			KeepAlive: ccUploadKeepAlive,
		}).Dial,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: *skipCertVerify,
		},
		TLSHandshakeTimeout: ccUploadTLSHandshakeTimeout,
	}

	pollerHttpClient := cf_http.NewClient()
	pollerHttpClient.Transport = transport

	uploader := ccclient.NewUploader(logger, &http.Client{Transport: transport})
	poller := ccclient.NewPoller(logger, pollerHttpClient, *ccJobPollingInterval)

	ccUploaderHandler, err := handlers.New(uploader, poller, logger)
	if err != nil {
		logger.Error("router-building-failed", err)
		os.Exit(1)
	}

	return http_server.New(*serverAddress, ccUploaderHandler)
}

func initializeRegistrationRunner(logger lager.Logger, consulClient *api.Client, listenAddress string, clock clock.Clock) ifrit.Runner {
	_, portString, err := net.SplitHostPort(listenAddress)
	if err != nil {
		logger.Fatal("failed-invalid-listen-address", err)
	}
	portNum, err := net.LookupPort("tcp", portString)
	if err != nil {
		logger.Fatal("failed-invalid-listen-port", err)
	}

	registration := &api.AgentServiceRegistration{
		Name: "cc-uploader",
		Port: portNum,
		Check: &api.AgentServiceCheck{
			TTL: "3s",
		},
	}

	return locket.NewRegistrationRunner(logger, registration, consuladapter.NewConsulClient(consulClient), locket.RetryInterval, clock)
}
