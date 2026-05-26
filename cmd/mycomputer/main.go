package main

import (
	"errors"
	"os"

	"github.com/1broseidon/mc/internal/contract"
)

var (
	version = "dev"
	commit  = "unknown"
	built   = "unknown"
)

func main() {
	if err := execute(); err != nil {
		if !errors.Is(err, errAlreadyPrinted) {
			writeError(os.Stderr, rootOpts.JSON, err)
		}
		os.Exit(contract.ErrorCode(err))
	}
}
