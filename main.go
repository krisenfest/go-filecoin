package main

import (
	"os"
	"strconv"

	oldlogging "gx/ipfs/QmcaSwFc5RBg8yCq54QURwEU4nwjfCpjbpmaAm4VbdGLKv/go-logging"
	logging "gx/ipfs/QmcuXC5cxs79ro2cUuHs4HQ2bkDLJUYokwL8aivcX6HW3C/go-log"

	"github.com/filecoin-project/go-filecoin/commands"
	"github.com/filecoin-project/go-filecoin/metrics"
)

func main() {
	// TODO: make configurable - this should be done via a command like go-ipfs
	// something like:
	//		`go-filecoin log level "system" "level"`
	// TODO: find a better home for this
	// TODO fix this in go-log 4 == INFO
	n, err := strconv.Atoi(os.Getenv("GO_FILECOIN_LOG_LEVEL"))
	if err != nil {
		n = 3
	}

	if os.Getenv("GO_FILECOIN_LOG_JSON") == "1" {
		oldlogging.SetFormatter(&metrics.JSONFormatter{})
	}

	logging.SetAllLoggers(oldlogging.Level(n))

	logging.SetLogLevel("dht", "error")          // nolint: errcheck
	logging.SetLogLevel("bitswap", "error")      // nolint: errcheck
	logging.SetLogLevel("heartbeat", "error")    // nolint: errcheck
	logging.SetLogLevel("blockservice", "error") // nolint: errcheck
	logging.SetLogLevel("peerqueue", "error")    // nolint: errcheck
	logging.SetLogLevel("swarm", "error")        // nolint: errcheck
	logging.SetLogLevel("swarm2", "error")       // nolint: errcheck
	logging.SetLogLevel("basichost", "error")    // nolint: errcheck
	logging.SetLogLevel("dht_net", "error")      // nolint: errcheck

	// TODO implement help text like so:
	// https://github.com/ipfs/go-ipfs/blob/master/core/commands/root.go#L91
	// TODO don't panic if run without a command.
	code, _ := commands.Run(os.Args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
