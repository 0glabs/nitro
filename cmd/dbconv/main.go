package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/cmd/dbconv/dbconv"
	"github.com/offchainlabs/nitro/cmd/genericconf"
	"github.com/offchainlabs/nitro/cmd/util/confighelpers"
	flag "github.com/spf13/pflag"
)

func parseDBConv(args []string) (*dbconv.DBConvConfig, error) {
	f := flag.NewFlagSet("dbconv", flag.ContinueOnError)
	dbconv.DBConvConfigAddOptions(f)
	k, err := confighelpers.BeginCommonParse(f, args)
	if err != nil {
		return nil, err
	}
	var config dbconv.DBConvConfig
	if err := confighelpers.EndCommonParse(k, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func printSampleUsage(name string) {
	fmt.Printf("Sample usage: %s [OPTIONS] \n\n", name)
	fmt.Printf("Options:\n")
	fmt.Printf("  --help\n")
	fmt.Printf("  --src.db-engine <leveldb or pebble>\n")
	fmt.Printf("  --src.data <source database directory>\n")
	fmt.Printf("  --dst.db-engine <leveldb or pebble>\n")
	fmt.Printf("  --dst.data <destination database directory>\n")
}
func main() {
	args := os.Args[1:]
	config, err := parseDBConv(args)
	if err != nil {
		confighelpers.PrintErrorAndExit(err, printSampleUsage)
		return
	}
	err = genericconf.InitLog("plaintext", log.Lvl(config.LogLevel), &genericconf.FileLoggingConfig{Enable: false}, nil)
	if err != nil {
		log.Error("Failed to init logging", "err", err)
		return
	}

	if err = config.Validate(); err != nil {
		log.Error("Invalid config", "err", err)
		return
	}
	conv := dbconv.NewDBConverter(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stats := conv.Stats()
				fmt.Printf("Progress:\n\tprocessed entries: %v\n\tprocessed data (MB): %v\n\telapsed: %v\n\tcurrent:\tentr/s: %v\tMB/s: %v\n\taverage:\tentr/s: %v\tMB/s: %v\n\tthreads: %v\tforks: %v\n", stats.Entries(), stats.Bytes()/1024/1024, stats.Elapsed(), stats.EntriesPerSecond(), stats.BytesPerSecond()/1024/1024, stats.AverageEntriesPerSecond(), stats.AverageBytesPerSecond()/1024/1024, stats.Threads(), stats.Forks())

			case <-ctx.Done():
				return
			}
		}
	}()

	if !config.VerifyOnly {
		err = conv.Convert(ctx)
		if err != nil {
			log.Error("Conversion error", "err", err)
			return
		}
		stats := conv.Stats()
		log.Info("Conversion finished.", "entries", stats.Entries(), "MB", stats.Bytes()/1024/1024, "avg e/s", stats.AverageEntriesPerSecond(), "avg MB/s", stats.AverageBytesPerSecond()/1024/1024, "elapsed", stats.Elapsed())
	}

	if config.Verify > 0 {
		err = conv.Verify(ctx)
		if err != nil {
			log.Error("Verification error", "err", err)
			return
		}
		stats := conv.Stats()
		log.Info("Verification completed successfully.", "elapsed:", stats.Elapsed())
	}
}
