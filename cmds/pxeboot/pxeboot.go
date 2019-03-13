// Copyright 2017-2018 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"time"

	"github.com/u-root/u-root/pkg/dhclient"
	"github.com/u-root/u-root/pkg/pxe"
	"github.com/vishvananda/netlink"
)

var (
	dryRun = flag.Bool("dry-run", false, "download kernel, but don't kexec it")
)

const (
	dhcpTimeout = 15 * time.Second
	dhcpTries   = 3
)

// Netboot boots all interfaces matched by the regex in ifaceNames.
func Netboot(ifaceNames string) error {
	ifs, err := netlink.LinkList()
	if err != nil {
		return err
	}

	var filteredIfs []netlink.Link
	ifregex := regexp.MustCompilePOSIX(ifaceNames)
	for _, iface := range ifs {
		if ifregex.MatchString(iface.Attrs().Name) {
			filteredIfs = append(filteredIfs, iface)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dhcpTries*dhcpTimeout)
	defer cancel()

	r := dhclient.SendRequests(ctx, filteredIfs, dhcpTimeout, dhcpTries, true, true)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case result, ok := <-r:
			if !ok {
				log.Printf("Configured all interfaces.")
				return fmt.Errorf("nothing bootable found")
			}
			if result.Err == nil {
				cancel()
				if err := Boot(result.Lease); err != nil {
					log.Printf("Failed to boot lease %v: %v", result.Lease, err)
					continue
				} else {
					return nil
				}
			}
		}
	}
}

func Boot(lease dhclient.Lease) error {
	if err := lease.Configure(); err != nil {
		return err
	}

	uri, err := lease.Boot()
	if err != nil {
		return err
	}
	log.Printf("Boot URI: %s", uri)

	wd := &url.URL{
		Scheme: uri.Scheme,
		Host:   uri.Host,
		Path:   path.Dir(uri.Path),
	}
	pc := pxe.NewConfig(wd)

	// IP only makes sense for v4 anyway.
	var ip net.IP
	if p4, ok := lease.(*dhclient.Packet4); ok {
		ip = p4.Lease().IP
	}
	if err := pc.FindConfigFile(lease.Link().Attrs().HardwareAddr, ip); err != nil {
		return fmt.Errorf("failed to parse pxelinux config: %v", err)
	}

	label := pc.Entries[pc.DefaultEntry]
	log.Printf("Got configuration: %s", label)

	if *dryRun {
		label.ExecutionInfo(log.New(os.Stderr, "", log.LstdFlags))
		return nil
	} else if err := label.Execute(); err != nil {
		return fmt.Errorf("kexec of %v failed: %v", label, err)
	}

	// Kexec should either return an error or not return.
	panic("unreachable")
}

func main() {
	flag.Parse()

	if err := Netboot("eth0"); err != nil {
		log.Fatal(err)
	}
}
