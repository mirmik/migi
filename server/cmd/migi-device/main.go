// Command migi-device performs local administration of paired devices.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mirmik/migi/server/internal/events"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databasePath := flag.String("db", "migi.db", "SQLite event journal path")
	list := flag.Bool("list", false, "list paired devices")
	revoke := flag.String("revoke", "", "device ID to revoke")
	flag.Parse()
	if (*list && *revoke != "") || (!*list && *revoke == "") {
		return errors.New("exactly one of -list or -revoke DEVICE_ID is required")
	}
	journal, err := events.OpenSQLite(*databasePath)
	if err != nil {
		return err
	}
	defer journal.Close()
	if *list {
		devices, err := journal.ListDevices(context.Background())
		if err != nil {
			return err
		}
		fmt.Println("DEVICE ID\tNAME\tLAST SEEN\tSTATUS")
		for _, device := range devices {
			status := "active"
			if device.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", device.ID, device.Name, device.LastSeenAt.Format("2006-01-02T15:04:05Z07:00"), status)
		}
		return nil
	}
	if err := journal.RevokeDevice(context.Background(), *revoke); err != nil {
		return err
	}
	fmt.Printf("revoked device %s\n", *revoke)
	return nil
}
